package baas

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/n-popescu/grid0plus/internal/db"
)

// The bot API is how the Discord bot (n-popescu/grid0plus-discord) manages
// accounts: registration, password resets, friends, avatars, and read-only
// views of who is online and whether GRID0+ is up. It is the only way to
// create a person's account.
//
// It listens on its own address, never the console-facing one, and every
// request must carry the shared token from baas.bot_api.token. The caller is
// named by Discord user id in X-Discord-User-ID; that is trusted only
// because the token proves the request comes from the bot, which takes it
// from Discord's own interaction payload, never from anything a person typed.
// Identity stays fail-closed: an unknown Discord id is 404, never a fallback.
//
// One Discord account owns several GRID0+ accounts. The first one it creates
// is the main account, the one commands that name no account act on; the rest
// are extras the person reaches by naming their login. A password reset
// resolves its target through that ownership and refuses anything else, so
// naming a login can only ever reach the caller's own accounts.

// ServiceStatus is one service's health, as the bot shows it.
type ServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// BotAPIOptions configures the bot API.
type BotAPIOptions struct {
	// Token is the shared secret the bot sends as a bearer token.
	Token string
	// Health reports each service's health for /status. Optional.
	Health func(ctx context.Context) []ServiceStatus
	// SaveRecordPrefix is the Ugcstore name prefix of Splatoon 3's cloud save
	// records, "tenants/<tenant>/saveRecords/". Empty disables the wipe.
	SaveRecordPrefix string
	// SaveBackupDir is where a wiped save record is archived first.
	SaveBackupDir string
}

// BotHandler serves the bot API.
func (s *Server) BotHandler(opts BotAPIOptions) http.Handler {
	b := &botAPI{s: s, token: opts.Token, health: opts.Health,
		saveRecordPrefix: opts.SaveRecordPrefix, saveBackupDir: opts.SaveBackupDir}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bot/v1/register", b.register)
	mux.HandleFunc("POST /bot/v1/link", b.link)
	mux.HandleFunc("POST /bot/v1/reset-password", b.withDiscordID(b.resetPassword))
	mux.HandleFunc("GET /bot/v1/accounts", b.withDiscordID(b.accounts))
	mux.HandleFunc("GET /bot/v1/me", b.withUser(b.me))
	mux.HandleFunc("PUT /bot/v1/avatar", b.withUser(b.avatar))
	mux.HandleFunc("GET /bot/v1/friends", b.withUser(b.friends))
	mux.HandleFunc("DELETE /bot/v1/friends/{userId}", b.withUser(b.removeFriend))
	mux.HandleFunc("GET /bot/v1/friend-requests", b.withUser(b.friendRequests))
	mux.HandleFunc("POST /bot/v1/friend-requests", b.withUser(b.sendFriendRequest))
	mux.HandleFunc("POST /bot/v1/friend-requests/{id}/{action}", b.withUser(b.settleFriendRequest))
	mux.HandleFunc("GET /bot/v1/users/by-discord/{discordId}", b.lookupDiscordUser)
	mux.HandleFunc("GET /bot/v1/online", b.online)
	mux.HandleFunc("GET /bot/v1/status", b.status)
	mux.HandleFunc("POST /bot/v1/splatoon3/save/wipe", b.withDiscordID(b.wipeSplatoon3Save))
	return b.authenticate(mux)
}

type botAPI struct {
	s      *Server
	token  string
	health func(ctx context.Context) []ServiceStatus

	saveRecordPrefix string
	saveBackupDir    string
}

// botError is the API's one error shape. Code is stable for the bot to switch
// on; Message is for logs.
type botError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeBotError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, botError{Code: code, Message: message})
}

func (b *botAPI) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if b.token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(b.token)) != 1 {
			writeBotError(w, http.StatusUnauthorized, "unauthorized", "bad or missing bot token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withDiscordID resolves the caller, by Discord user id, to nothing but the
// id: the handler picks which of that Discord account's accounts to act on.
func (b *botAPI) withDiscordID(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		discordID := r.Header.Get("X-Discord-User-ID")
		if !discordIDPattern.MatchString(discordID) {
			writeBotError(w, http.StatusBadRequest, "bad_discord_id", "missing or malformed X-Discord-User-ID")
			return
		}
		h(w, r, discordID)
	}
}

// withUser is withDiscordID for the endpoints that act on one account: it
// resolves the caller's main account, and refuses a Discord account that owns
// none. Still fail-closed, never a fallback to somebody else's account.
func (b *botAPI) withUser(h func(http.ResponseWriter, *http.Request, db.User)) http.HandlerFunc {
	return b.withDiscordID(func(w http.ResponseWriter, r *http.Request, discordID string) {
		user, err := b.s.store.GetUserByDiscordID(r.Context(), discordID)
		switch {
		case errors.Is(err, db.ErrNotFound) || (err == nil && user.Deleted):
			writeBotError(w, http.StatusNotFound, "not_registered", "this Discord account has no GRID0+ account")
			return
		case err != nil:
			b.internal(w, "load user by discord id", err)
			return
		}
		h(w, r, user)
	})
}

// hasMainAccount reports whether a Discord account already owns a main
// account, which is what decides whether the next one it makes is that main
// account or an extra beside it.
func (b *botAPI) hasMainAccount(ctx context.Context, discordUserID string) (bool, error) {
	switch _, err := b.s.store.GetUserByDiscordID(ctx, discordUserID); {
	case err == nil:
		return true, nil
	case errors.Is(err, db.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

func (b *botAPI) internal(w http.ResponseWriter, what string, err error) {
	b.s.log.Error("botapi: "+what, "error", err)
	writeBotError(w, http.StatusInternalServerError, "internal", what)
}

// Discord snowflakes are decimal, 17 to 20 digits.
var discordIDPattern = regexp.MustCompile(`^[0-9]{15,21}$`)

// A GRID0+ login. Discord usernames since 2023 are lowercase letters, digits,
// '_' and '.', and a login a person chooses in Discord is held to the same
// shape: it has to be typeable on a Switch's on-screen keyboard, and one rule
// for both is better than a second one nobody has evidence for.
var loginUsernamePattern = regexp.MustCompile(`^[a-z0-9_.]{2,32}$`)

// ---- registration and credentials ------------------------------------------

type registerRequest struct {
	DiscordUserID string `json:"discordUserId"`
	Username      string `json:"username"`
	DisplayName   string `json:"displayName"`
	// Additional is set when the person named this login themselves, which is
	// the only way a Discord account that already has one may make another.
	Additional bool `json:"additional"`
}

type credentialsResponse struct {
	UserID     string `json:"userId"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	FriendCode string `json:"friendCode,omitempty"`
}

func (b *botAPI) register(w http.ResponseWriter, r *http.Request) {
	if !b.s.cfg.AllowRegistration {
		writeBotError(w, http.StatusForbidden, "registration_closed", "registration is disabled")
		return
	}
	var req registerRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	// Its own code, so the bot can tell the person what to change: a login
	// with a capital, a dash or a space used to fail as a generic
	// bad_request, and the bot said only "something went wrong" (2026-10-11).
	if !discordIDPattern.MatchString(req.DiscordUserID) {
		writeBotError(w, http.StatusBadRequest, "bad_request", "malformed Discord id")
		return
	}
	if !loginUsernamePattern.MatchString(req.Username) {
		writeBotError(w, http.StatusBadRequest, "bad_username",
			"a login is 2 to 32 characters: lowercase letters, digits, '_' and '.'")
		return
	}
	hasMain, err := b.hasMainAccount(r.Context(), req.DiscordUserID)
	if err != nil {
		b.internal(w, "look up accounts for discord id", err)
		return
	}
	if hasMain && !req.Additional {
		// The default /register mints one account from the person's own
		// Discord username, so a second one only makes sense if they asked
		// for it by name.
		writeBotError(w, http.StatusConflict, "already_registered", "this Discord account already has a GRID0+ account")
		return
	}

	password, err := newHumanPassword()
	if err != nil {
		b.internal(w, "generate password", err)
		return
	}
	nickname := nicknameFrom(req.DisplayName, req.Username)
	user, err := b.s.createLoginUser(r.Context(), req.Username, nickname, password, req.DiscordUserID, !hasMain)
	switch {
	case errors.Is(err, db.ErrConflict):
		writeBotError(w, http.StatusConflict, "username_taken", "that username is already a GRID0+ login")
		return
	case err != nil:
		b.internal(w, "create user", err)
		return
	}
	b.s.log.Info("botapi: registered", "user", user.ID, "login_username", user.LoginUsername, "main_account", !hasMain)

	fc, _ := b.s.store.GetFriendCode(r.Context(), user.ID)
	writeJSON(w, http.StatusCreated, credentialsResponse{
		UserID: user.ID, Username: user.LoginUsername, Password: password, FriendCode: fc.Code,
	})
}

type linkRequest struct {
	DiscordUserID string `json:"discordUserId"`
	Username      string `json:"username"`
	Password      string `json:"password"`
}

// link claims an account made before the bot existed, by its login and
// password, for the Discord account asking. Without it those people, with
// their consoles, friends and game data, could never use the bot.
func (b *botAPI) link(w http.ResponseWriter, r *http.Request) {
	var req linkRequest
	if err := decodeJSONBody(r, &req); err != nil || !discordIDPattern.MatchString(req.DiscordUserID) ||
		req.Username == "" || req.Password == "" {
		writeBotError(w, http.StatusBadRequest, "bad_request", "need a Discord id, a username and a password")
		return
	}
	// Same budget as every other password check, so /link cannot be used to
	// guess a login's password faster than the login page can.
	if !b.s.loginLimiter.Allow("discord:"+req.DiscordUserID, b.s.now()) {
		writeBotError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts; wait a while")
		return
	}
	hasMain, err := b.hasMainAccount(r.Context(), req.DiscordUserID)
	if err != nil {
		b.internal(w, "look up accounts for discord id", err)
		return
	}

	user, err := b.s.store.GetUserByLoginUsername(r.Context(), req.Username)
	if err == nil && !user.Deleted && user.LoginPasswordHash != "" &&
		bcrypt.CompareHashAndPassword([]byte(user.LoginPasswordHash), []byte(req.Password)) == nil {
		// Re-linking an account this Discord account already owns is a no-op,
		// not a refusal: running /link twice must not be an error.
		if user.DiscordUserID != "" && user.DiscordUserID != req.DiscordUserID {
			writeBotError(w, http.StatusConflict, "claimed", "that account is already linked to another Discord account")
			return
		}
		if err := b.s.store.LinkBotAccount(r.Context(), req.DiscordUserID, user.ID, !hasMain); err != nil {
			if errors.Is(err, db.ErrConflict) {
				writeBotError(w, http.StatusConflict, "claimed", "that account is already linked to another Discord account")
				return
			}
			b.internal(w, "link discord", err)
			return
		}
		b.s.log.Info("botapi: linked an existing account", "user", user.ID, "login_username", user.LoginUsername, "main_account", !hasMain)
		writeJSON(w, http.StatusOK, b.describe(r.Context(), user, false))
		return
	}
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		b.internal(w, "load user by login", err)
		return
	}
	// One answer for "no such login" and "wrong password", as on the login page.
	b.s.log.Warn("botapi: link refused", "login_username", req.Username)
	writeBotError(w, http.StatusUnauthorized, "bad_credentials", "wrong login or password")
}

func (b *botAPI) resetPassword(w http.ResponseWriter, r *http.Request, discordID string) {
	var req struct {
		Username              string `json:"username"`
		DisconnectAllAccounts *bool  `json:"disconnectAllAccounts"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSONBody(r, &req); err != nil {
			writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
			return
		}
	}
	// Which account the person means: the one they named, or their main
	// account. A named login is resolved through the caller's own accounts, so
	// this can only ever reach an account of theirs -- a login that does not
	// exist and a login that belongs to somebody else are one refusal, which
	// is also what keeps a login's existence from being a public fact.
	user, err := b.s.store.GetUserByDiscordID(r.Context(), discordID)
	switch {
	case errors.Is(err, db.ErrNotFound) || (err == nil && user.Deleted):
		writeBotError(w, http.StatusNotFound, "not_registered", "this Discord account has no GRID0+ account")
		return
	case err != nil:
		b.internal(w, "load user by discord id", err)
		return
	}
	if req.Username != "" {
		user, err = b.s.store.GetUserByDiscordLogin(r.Context(), discordID, req.Username)
		switch {
		case errors.Is(err, db.ErrNotFound) || (err == nil && user.Deleted):
			writeBotError(w, http.StatusNotFound, "not_your_account", "that login is not one of your accounts")
			return
		case err != nil:
			b.internal(w, "load your account by login", err)
			return
		}
	}

	disconnect := req.DisconnectAllAccounts == nil || *req.DisconnectAllAccounts
	password, err := newHumanPassword()
	if err != nil {
		b.internal(w, "generate password", err)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		b.internal(w, "hash password", err)
		return
	}
	h := string(hash)
	if _, err := b.s.store.UpdateUser(r.Context(), user.ID, db.UserPatch{LoginPasswordHash: &h}); err != nil {
		b.internal(w, "store password", err)
		return
	}
	// A password reset may be for forgotten credentials rather than a compromised
	// account. Keep existing console tokens only when the owner explicitly opts out.
	revoked := 0
	if disconnect {
		now := b.s.now()
		das, err := b.s.store.ListDeviceAccounts(r.Context(), user.ID)
		if err != nil {
			b.internal(w, "list device accounts", err)
			return
		}
		for _, da := range das {
			if err := b.s.store.RevokeDeviceAccountTokens(r.Context(), da.ID, now); err != nil {
				b.internal(w, "revoke tokens", err)
				return
			}
		}
		revoked = len(das)
	}
	b.s.log.Info("botapi: password reset", "user", user.ID, "login_username", user.LoginUsername, "revoked_device_accounts", revoked)
	writeJSON(w, http.StatusOK, credentialsResponse{
		UserID: user.ID, Username: user.LoginUsername, Password: password,
	})
}

// humanPasswordAlphabet leaves out 0/O and 1/l/I: the password is read off a
// Discord message and typed on a Switch keyboard.
const humanPasswordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func newHumanPassword() (string, error) {
	out := make([]byte, 8)
	max := big.NewInt(int64(len(humanPasswordAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = humanPasswordAlphabet[n.Int64()]
	}
	return string(out), nil
}

// nicknameFrom picks the Switch nickname: the Discord display name if it
// fits the console's 10-character limit, else the username, cut to fit.
func nicknameFrom(displayName, username string) string {
	for _, n := range []string{strings.TrimSpace(displayName), username} {
		if n == "" {
			continue
		}
		runes := []rune(n)
		if len(runes) > 10 {
			runes = runes[:10]
		}
		return string(runes)
	}
	return username
}

// ---- profile -------------------------------------------------------------

type botUser struct {
	UserID        string `json:"userId"`
	DiscordUserID string `json:"discordUserId,omitempty"`
	Username      string `json:"username,omitempty"`
	Nickname      string `json:"nickname"`
	FriendCode    string `json:"friendCode,omitempty"`
	Presence      string `json:"presence,omitempty"`
	AppID         string `json:"appId,omitempty"`
}

type meResponse struct {
	botUser
	Consoles     int       `json:"consoles"`
	Friends      int       `json:"friends"`
	RegisteredAt time.Time `json:"registeredAt"`
}

func (b *botAPI) describe(ctx context.Context, u db.User, withPresence bool) botUser {
	out := botUser{
		UserID: u.ID, DiscordUserID: u.DiscordUserID,
		Username: u.LoginUsername, Nickname: u.Nickname,
	}
	if fc, err := b.s.store.GetFriendCode(ctx, u.ID); err == nil {
		out.FriendCode = fc.Code
	}
	if withPresence {
		if p, err := b.s.effectivePresence(ctx, u.ID); err == nil {
			out.Presence, out.AppID = p.State, p.AppID
		}
	}
	return out
}

func (b *botAPI) me(w http.ResponseWriter, r *http.Request, user db.User) {
	das, err := b.s.store.ListDeviceAccounts(r.Context(), user.ID)
	if err != nil {
		b.internal(w, "list device accounts", err)
		return
	}
	friends, err := b.s.store.ListFriends(r.Context(), user.ID, 1000)
	if err != nil {
		b.internal(w, "list friends", err)
		return
	}
	writeJSON(w, http.StatusOK, meResponse{
		botUser:  b.describe(r.Context(), user, true),
		Consoles: len(das), Friends: len(friends), RegisteredAt: user.CreatedAt,
	})
}

// accounts lists the caller's own accounts, so the bot can offer them for
// /reset-password. Its own accounts only: this is the same lookup a reset
// resolves its target through, so offering anybody else's logins here would be
// exactly the leak that lookup refuses. The main account comes first, which is
// the order a person should pick from.
func (b *botAPI) accounts(w http.ResponseWriter, r *http.Request, discordID string) {
	list, err := b.s.store.ListUsersByDiscordID(r.Context(), discordID)
	if err != nil {
		b.internal(w, "list your accounts", err)
		return
	}
	out := make([]botAccount, 0, len(list))
	for i, u := range list {
		out = append(out, botAccount{UserID: u.ID, Username: u.LoginUsername, Primary: i == 0})
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

type botAccount struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Primary  bool   `json:"primary"`
}

// lookupDiscordUser tells the bot whether a mentioned Discord account is
// registered, and under which GRID0+ user. Public details only.
func (b *botAPI) lookupDiscordUser(w http.ResponseWriter, r *http.Request) {
	u, err := b.s.store.GetUserByDiscordID(r.Context(), r.PathValue("discordId"))
	switch {
	case errors.Is(err, db.ErrNotFound) || (err == nil && u.Deleted):
		writeBotError(w, http.StatusNotFound, "not_registered", "that Discord account has no GRID0+ account")
		return
	case err != nil:
		b.internal(w, "load user by discord id", err)
		return
	}
	writeJSON(w, http.StatusOK, b.describe(r.Context(), u, false))
}

// avatar makes a JPEG the caller's Switch profile picture. The previous
// bot-set picture's file is removed, so re-syncing never piles up images.
func (b *botAPI) avatar(w http.ResponseWriter, r *http.Request, user db.User) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, b.s.cfg.MaxImageBytes+1))
	if err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	if int64(len(raw)) > b.s.cfg.MaxImageBytes {
		writeBotError(w, http.StatusRequestEntityTooLarge, "too_large", "image is too large")
		return
	}
	previous := user.ThumbnailURL
	_, thumb, err := b.s.storeProfileImage(r.Context(), user.ID, raw)
	switch {
	case errors.Is(err, errNotAJPEG):
		writeBotError(w, http.StatusBadRequest, "not_jpeg", "the image must be a JPEG")
		return
	case err != nil:
		b.internal(w, "store avatar", err)
		return
	}
	b.removeReplacedImage(previous)
	b.s.log.Info("botapi: avatar set", "user", user.ID, "bytes", len(raw))
	writeJSON(w, http.StatusOK, map[string]string{"url": thumb})
}

// storedImageName is the only file shape storeProfileImage writes.
var storedImageName = regexp.MustCompile(`^[0-9a-f]{32}\.jpg$`)

func (b *botAPI) removeReplacedImage(thumbURL string) {
	name := path.Base(thumbURL)
	if thumbURL == "" || !storedImageName.MatchString(name) {
		// The shared default avatar, or nothing: never delete those.
		return
	}
	if err := os.Remove(filepath.Join(b.s.cfg.ImageDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		b.s.log.Warn("botapi: could not remove replaced avatar", "file", name, "error", err)
	}
}

// ---- friends ---------------------------------------------------------------

func (b *botAPI) friends(w http.ResponseWriter, r *http.Request, user db.User) {
	list, err := b.s.store.ListFriends(r.Context(), user.ID, 1000)
	if err != nil {
		b.internal(w, "list friends", err)
		return
	}
	out := make([]botUser, 0, len(list))
	for _, f := range list {
		friend, err := b.s.store.GetUser(r.Context(), f.FriendID)
		if err != nil {
			continue
		}
		out = append(out, b.describe(r.Context(), friend, b.s.presenceVisibleTo(r.Context(), friend, user.ID)))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Nickname) < strings.ToLower(out[j].Nickname) })
	writeJSON(w, http.StatusOK, map[string]any{"friends": out})
}

func (b *botAPI) removeFriend(w http.ResponseWriter, r *http.Request, user db.User) {
	err := b.s.store.RemoveFriendship(r.Context(), user.ID, strings.ToLower(r.PathValue("userId")))
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeBotError(w, http.StatusNotFound, "not_friends", "you are not friends with that user")
		return
	case err != nil:
		b.internal(w, "remove friendship", err)
		return
	}
	b.s.notifyFriendDeleted(r.Context(), strings.ToLower(r.PathValue("userId")))
	b.s.log.Info("botapi: unfriended", "user", user.ID, "friend", r.PathValue("userId"))
	w.WriteHeader(http.StatusNoContent)
}

type botFriendRequest struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Other     botUser   `json:"other"`
	CreatedAt time.Time `json:"createdAt"`
}

func (b *botAPI) friendRequests(w http.ResponseWriter, r *http.Request, user db.User) {
	list := func(incoming bool) ([]botFriendRequest, error) {
		reqs, err := b.s.store.ListFriendRequests(r.Context(), user.ID, incoming, true)
		if err != nil {
			return nil, err
		}
		out := make([]botFriendRequest, 0, len(reqs))
		for _, fr := range reqs {
			otherID := fr.ToUserID
			if incoming {
				otherID = fr.FromUserID
			}
			other, err := b.s.store.GetUser(r.Context(), otherID)
			if err != nil {
				continue
			}
			out = append(out, botFriendRequest{
				ID: fr.ID, State: fr.State, Other: b.describe(r.Context(), other, false), CreatedAt: fr.CreatedAt,
			})
		}
		return out, nil
	}
	incoming, err := list(true)
	if err != nil {
		b.internal(w, "list incoming requests", err)
		return
	}
	outgoing, err := list(false)
	if err != nil {
		b.internal(w, "list outgoing requests", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"incoming": incoming, "outgoing": outgoing})
}

type sendRequestBody struct {
	DiscordUserID string `json:"discordUserId"`
	FriendCode    string `json:"friendCode"`
}

func (b *botAPI) sendFriendRequest(w http.ResponseWriter, r *http.Request, user db.User) {
	var body sendRequestBody
	if err := decodeJSONBody(r, &body); err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}

	var target db.User
	var err error
	switch {
	case body.DiscordUserID != "":
		target, err = b.s.store.GetUserByDiscordID(r.Context(), body.DiscordUserID)
	case body.FriendCode != "":
		code := normaliseFriendCode(body.FriendCode)
		if code == "" {
			// Never pass an empty code on: an empty filter is no filter, and
			// the request would go to whoever FindUsers returns first.
			writeBotError(w, http.StatusBadRequest, "bad_friend_code", "a friend code is twelve digits")
			return
		}
		var found []db.User
		found, err = b.s.store.FindUsers(r.Context(), db.UserQuery{FriendCode: code, Limit: 1})
		if err == nil && len(found) == 0 {
			err = db.ErrNotFound
		}
		if err == nil {
			target = found[0]
		}
	default:
		writeBotError(w, http.StatusBadRequest, "bad_request", "name a Discord user or a friend code")
		return
	}
	if errors.Is(err, db.ErrNotFound) || (err == nil && target.Deleted) {
		writeBotError(w, http.StatusNotFound, "no_such_user", "nobody on GRID0+ matches that")
		return
	}
	if err != nil {
		b.internal(w, "find friend request target", err)
		return
	}

	req, err := b.s.sendFriendRequest(r.Context(), user.ID, target)
	switch {
	case errors.Is(err, errSelfFriendRequest):
		writeBotError(w, http.StatusBadRequest, "self", "you cannot add yourself")
		return
	case errors.Is(err, errFriendRequestRefused):
		writeBotError(w, http.StatusForbidden, "refused", "that user is not accepting friend requests")
		return
	case errors.Is(err, errAlreadyFriends):
		writeBotError(w, http.StatusConflict, "already_friends", "you are already friends")
		return
	case err != nil:
		b.internal(w, "send friend request", err)
		return
	}
	result := "sent"
	if req.State == db.RequestAccepted {
		result = "became_friends"
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"result": result, "requestId": req.ID, "other": b.describe(r.Context(), target, false),
	})
}

func (b *botAPI) settleFriendRequest(w http.ResponseWriter, r *http.Request, user db.User) {
	states := map[string]string{
		"accept": db.RequestAccepted, "deny": db.RequestDenied, "cancel": db.RequestCancelled,
	}
	state, ok := states[r.PathValue("action")]
	if !ok {
		writeBotError(w, http.StatusNotFound, "bad_action", "action is accept, deny or cancel")
		return
	}
	req, err := b.s.settleFriendRequest(r.Context(), user.ID, r.PathValue("id"), state)
	switch {
	case errors.Is(err, db.ErrNotFound), errors.Is(err, errNotYourRequest):
		writeBotError(w, http.StatusNotFound, "no_such_request", "no such friend request for you")
		return
	case errors.Is(err, errRequestNotPending):
		writeBotError(w, http.StatusConflict, "not_pending", "that request was already answered")
		return
	case err != nil:
		b.internal(w, "settle friend request", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": req.ID, "state": req.State})
}

// ---- who is on, and is it up ---------------------------------------------------

// online lists everyone online or playing, except those whose presence
// setting is SELF: they asked not to be seen, and a Discord channel is a
// wider audience than their friend list.
func (b *botAPI) online(w http.ResponseWriter, r *http.Request) {
	out, err := b.onlineUsers(r.Context())
	if err != nil {
		b.internal(w, "list online users", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (b *botAPI) onlineUsers(ctx context.Context) ([]botUser, error) {
	users, err := b.s.store.ListUsers(ctx, 1000)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	presences, err := b.s.effectivePresences(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := []botUser{}
	for _, u := range users {
		p := presences[u.ID]
		if u.Deleted || u.Permissions.Presence == "SELF" || p.State == db.StateOffline || p.State == "" {
			continue
		}
		entry := b.describe(ctx, u, false)
		entry.Presence, entry.AppID = p.State, p.AppID
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Nickname) < strings.ToLower(out[j].Nickname) })
	return out, nil
}

func (b *botAPI) status(w http.ResponseWriter, r *http.Request) {
	var services []ServiceStatus
	if b.health != nil {
		services = b.health(r.Context())
	}
	online, err := b.onlineUsers(r.Context())
	if err != nil {
		b.internal(w, "count online users", err)
		return
	}
	stats, err := b.s.store.Stats(r.Context())
	if err != nil {
		b.internal(w, "stats", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"services": services, "online": len(online), "players": stats.Users - stats.DeletedUsers,
	})
}
