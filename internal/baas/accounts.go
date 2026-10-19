package baas

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/n-popescu/grid0plus/internal/db"
	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
	"github.com/n-popescu/grid0plus/internal/service"
)

// ---- POST /1.0.0/application/token --------------------------------------

// tokenResponse is the reply to /1.0.0/application/token.
type tokenResponse struct {
	AccessToken string `json:"accessToken"`
	TokenType   string `json:"tokenType"`
	ExpiresIn   int    `json:"expiresIn"`
}

// applicationToken exchanges a device token for an anonymous access token.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#post-100applicationtoken
//
// This is the first BAAS call a console makes and the only one that needs no
// access token. The device token it presents was issued by GRID0+'s own dauth
// server, so verifying it checks GRID0+'s signature — it is not a claim about
// the console being genuine.
func (s *Server) applicationToken(w http.ResponseWriter, r *http.Request) {
	form, err := parseForm(r)
	if err != nil {
		writeError(w, r, errInvalidParams)
		return
	}
	if form.Get("grantType") != "public_client" {
		writeError(w, r, errInvalidParams)
		return
	}
	assertion := form.Get("assertion")
	if assertion == "" {
		writeError(w, r, errInvalidParams)
		return
	}

	var device sjwt.DeviceTokenClaims
	if err := s.deviceVerifier.Verify(assertion, &device); err != nil {
		s.log.Warn("baas: rejected device token at /application/token", "error", err)
		writeError(w, r, errInvalidToken)
		return
	}

	now := s.now()
	ist := device.Nintendo.IsT
	token, err := s.accessIssuer.Sign(sjwt.BAASTokenClaims{
		Subject:   s.cfg.Audience,
		Audience:  s.cfg.Audience,
		IssuedAt:  now.Unix(),
		Expires:   now.Add(s.cfg.AccessTokenTTL).Unix(),
		Issuer:    s.cfg.Issuer,
		TokenID:   sjwt.NewTokenID(),
		Type:      "token",
		GrantType: 1,
		Status:    sjwt.AccessTokenStatus,
		Nintendo: sjwt.DeviceInfo{
			DeviceType:   device.Nintendo.DeviceType,
			ProductCode:  device.Nintendo.ProductCode,
			DeviceID:     device.Subject,
			SerialNumber: device.Nintendo.SerialNumber,
			IsT:          ist,
			// Echoed from the device token unchanged, as a working server does. See
			// DeviceInfo.X5tS256.
			X5tS256: device.Nintendo.X5tS256,
			// Esv likewise: this endpoint copies the device token's block
			// forward rather than minting a fresh value — see DeviceInfo's
			// package comment for why that is the copy semantics observed,
			// not evidence esv is a per-device constant.
			Esv: device.Nintendo.Esv,
		},
	})
	if err != nil {
		writeInternalError(s.log, w, r, "sign anonymous access token", err)
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int(s.cfg.AccessTokenTTL.Seconds()),
	})
}

// ---- POST /1.0.0/users ---------------------------------------------------

// registerUser creates an account and its first device account.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#post-100users
//
// The request has no parameters. The response carries the device account
// password exactly once; the console stores it and it can never be read back, so
// if that response is lost the account is unreachable from that console.
func (s *Server) registerUser(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.cfg.AllowRegistration {
		// Turned off once every console in the group is registered, so that
		// somebody who finds the server cannot add themselves to it.
		s.log.Warn("baas: refused registration, registration is disabled")
		writeError(w, r, errForbidden)
		return
	}

	userID, err := newHexID(8)
	if err != nil {
		writeInternalError(s.log, w, r, "generate user id", err)
		return
	}
	deviceAccountID, err := newHexID(8)
	if err != nil {
		writeInternalError(s.log, w, r, "generate device account id", err)
		return
	}
	password, err := newDevicePassword()
	if err != nil {
		writeInternalError(s.log, w, r, "generate device account password", err)
		return
	}

	now := s.now()
	user := db.User{
		ID:          userID,
		Birthday:    "0000-00-00",
		Permissions: db.DefaultPermissions(now),
		Extras:      db.EmptyExtras(),
		// A real account always carries a thumbnailUploadedAt; a self-registered
		// GRID0+ account never goes through an "upload a thumbnail" step, so it would
		// stay nil forever otherwise. Real accounts have a thumbnail (even the default
		// one) from the moment they exist, same reasoning as EmptyExtras' defaults.
		ThumbnailUploadedAt: &now,
		// This console's placeholder until it links to a real person's
		// account, which deletes it (db.Store.ReassignDeviceAccount).
		Provisional:      true,
		BlocksUpdatedAt:  now,
		FriendsUpdatedAt: now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	deviceAccount := db.DeviceAccount{
		ID:             deviceAccountID,
		UserID:         userID,
		PasswordSHA256: hashPassword(password),
		DeviceID:       c.DeviceID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.store.CreateUser(r.Context(), user, deviceAccount); err != nil {
		writeInternalError(s.log, w, r, "create user", err)
		return
	}

	s.log.Info("baas: registered user", "user", userID, "device_account", deviceAccountID)

	fc, err := assignFriendCode(r.Context(), s.store, now, userID, s.cfg.FriendCodeCooldown)
	if err != nil {
		writeInternalError(s.log, w, r, "assign friend code", err)
		return
	}

	thumb1, thumb2 := s.defaultAvatarURLOrEmpty()
	resp := userResponse(s.now(), user, &fc, db.Presence{
		UserID: userID, State: db.StateOffline, Extras: db.EmptyExtras(), ReportedAt: now,
	}, nil, visSelf, thumb1, thumb2)
	// The one and only time the password is sent.
	resp.DeviceAccounts = []DeviceAccountResponse{{ID: deviceAccountID, Password: password}}

	w.Header().Set("Location", "/1.0.0/users/"+userID)
	writeJSON(w, http.StatusCreated, resp)
}

// ---- POST /1.0.0/login ---------------------------------------------------

// loginResponse is the reply to /1.0.0/login and /1.0.0/federation.
type loginResponse struct {
	ExpiresIn   int          `json:"expiresIn"`
	User        UserResponse `json:"user"`
	IDToken     string       `json:"idToken"`
	AccessToken string       `json:"accessToken"`
	TokenType   string       `json:"tokenType"`
	// Summary is only present when an application token was supplied whose
	// policy handler is GAME_SERVER and the user has a membership.
	Summary *loginSummary `json:"summary,omitempty"`
}

type loginSummary struct {
	Nintendo loginSummaryNintendo `json:"nintendo"`
}

type loginSummaryNintendo struct {
	HasMembership bool `json:"hasMembership"`
}

// login authenticates a device account and issues the user's tokens.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#post-100login
//
// This is where identity is decided, and it is the one function in GRID0+
// that must never guess. The device account id and password name exactly one
// user. If they do not match, the answer is invalid_device_account — never a
// fallback to some other account.
func (s *Server) login(w http.ResponseWriter, r *http.Request, _ *caller) {
	form, err := parseForm(r)
	if err != nil {
		writeError(w, r, errInvalidParams)
		return
	}
	s.completeLogin(w, r, form, "", "")
}

// loginByUsername authenticates a person by the username and password they
// chose at self-registration (website.go, GET/POST /register) and issues the
// same tokens completeLogin would for a device account.
//
// For a client with no device account of its own to present -- an
// emulator's own account client, which has no console-issued credential and
// no way to get one, rather than a real console going through
// dauth/aauth/device_auth_token. Not a Nintendo-documented endpoint and does
// not try to look like one: no prior access token is required (unlike
// /1.0.0/login, which needs the anonymous one from /1.0.0/application/token
// -- there is nothing to be anonymous ABOUT here, the username already says
// who is asking), and its error responses are plain JSON via
// writePlainError, not a BAAS errorCode.
//
// Resolves to the person's own device account -- see
// deviceAccountForLogin -- rather than inventing a new kind of token. The
// password check below is what decides identity; everything after it is the
// same issueLoginTokens completeLogin already uses; this function has no
// device-account-shaped bytes of its own.
func (s *Server) loginByUsername(w http.ResponseWriter, r *http.Request) {
	form, err := parseForm(r)
	if err != nil {
		writeError(w, r, errInvalidParams)
		return
	}

	username := strings.TrimSpace(form.Get("username"))
	password := form.Get("password")
	if username == "" || password == "" {
		writeError(w, r, errInvalidParams)
		return
	}

	// Same limiter /1.0.0/login uses. A chosen username is far easier to
	// guess than a random device account id, so this needs the throttle at
	// least as much.
	if !s.loginLimiter.Allow(service.ClientIP(r), s.now()) {
		s.log.Warn("baas: login rate limit exceeded", "client", service.ClientIP(r))
		writeRateLimited(w, s.cfg.LoginWindow)
		return
	}

	user, err := s.store.GetUserByLoginUsername(r.Context(), username)
	switch {
	case errors.Is(err, db.ErrNotFound):
		// Same answer as a wrong password below: telling the two apart would
		// tell a guesser which usernames exist.
		s.log.Warn("baas: login for unknown username")
		writePlainError(w, http.StatusUnauthorized, "invalid username or password")
		return
	case err != nil:
		writeInternalError(s.log, w, r, "load user by login username", err)
		return
	}

	if user.LoginPasswordHash == "" ||
		bcrypt.CompareHashAndPassword([]byte(user.LoginPasswordHash), []byte(password)) != nil {
		s.log.Warn("baas: login with wrong password", "user", user.ID)
		writePlainError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	if user.Deleted {
		writePlainError(w, http.StatusForbidden, "account is deleted")
		return
	}

	s.loginLimiter.Succeeded(service.ClientIP(r))

	da, err := s.deviceAccountForLogin(r.Context(), user.ID)
	if err != nil {
		writeInternalError(s.log, w, r, "resolve device account for username login", err)
		return
	}

	s.issueLoginTokens(w, r, form, da, "")
}

// deviceAccountForLogin returns a user's own device account for
// loginByUsername to issue tokens against, minting one if they have none
// yet -- the case for every person who registered on the website
// (website.go's CreateUserWithLogin, deliberately with no device account at
// all) and has never linked a real console since.
//
// The oldest one, when there is a choice: deterministic, so the same person
// logging in twice gets tokens naming the same device account both times,
// rather than whichever one a map happened to return first.
//
// Not guarded against two concurrent first-ever logins for the same brand
// new user both seeing no device account and each minting their own: this
// server is sized for a private group of consoles one operator runs, not a
// public service, and the outcome is a harmless second device account for
// the same person -- ListDeviceAccounts's ordering makes every login after
// the race resolve to the same (oldest) one again -- not a security
// problem or a wrong identity, so a lock to close it is not worth carrying.
func (s *Server) deviceAccountForLogin(ctx context.Context, userID string) (db.DeviceAccount, error) {
	das, err := s.store.ListDeviceAccounts(ctx, userID)
	if err != nil {
		return db.DeviceAccount{}, err
	}
	if len(das) > 0 {
		return das[0], nil
	}

	deviceAccountID, err := newHexID(8)
	if err != nil {
		return db.DeviceAccount{}, err
	}
	// Generated and immediately discarded: nothing outside this function
	// ever needs this device account's password, because it is only ever
	// reached through the website username/password that got us here, never
	// through /1.0.0/login's own id+password.
	password, err := newDevicePassword()
	if err != nil {
		return db.DeviceAccount{}, err
	}

	now := s.now()
	da := db.DeviceAccount{
		ID:             deviceAccountID,
		UserID:         userID,
		PasswordSHA256: hashPassword(password),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.CreateDeviceAccountForUser(ctx, da); err != nil {
		return db.DeviceAccount{}, err
	}

	s.log.Info("baas: minted a device account for a username login", "user", userID, "device_account", da.ID)
	return da, nil
}

// federation is /1.0.0/login plus a Nintendo Account id token.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#post-100federation
//
// A REAL Nintendo Account id token cannot be verified here, because GRID0+ does
// not have Nintendo's key, so in general it is treated as a label, not a credential:
// its subject is recorded as a linked account id (see recordNintendoAccountLink) and
// grants nothing on its own. Authentication is still the device account id and
// password, exactly as in /1.0.0/login.
//
// One case is different: a token that verifies against GRID0+'s own idIssuer key can
// only exist because nalink.go minted it, for the person who just signed in on the
// Nintendo Account link page. Its subject is a real, existing GRID0+ user id, not an
// opaque label, and this is the only place that reassigns the device account
// /1.0.0/users just created to that person, rather than leaving the console
// permanently attached to the fresh, empty identity registerUser had to invent because
// it runs before any id token exists. See completeLogin's use of idVerifier. Without
// this, the console federates successfully, but the "logged in" identity it ends up
// with is not the one whose credentials it just typed into the login page, and it gives
// up soon after (error 2124-0292) with nothing further logged.
func (s *Server) federation(w http.ResponseWriter, r *http.Request, _ *caller) {
	form, err := parseForm(r)
	if err != nil {
		writeError(w, r, errInvalidParams)
		return
	}
	if form.Get("idp") != "nintendoAccount" {
		writeError(w, r, errInvalidParams)
		return
	}

	var naID, verifiedNAID string
	if idToken := form.Get("idToken"); idToken != "" {
		var vclaims sjwt.BAASTokenClaims
		if err := s.idVerifier.Verify(idToken, &vclaims); err == nil {
			naID = vclaims.Subject
			verifiedNAID = vclaims.Subject
		} else {
			// Not one of ours — either a real Nintendo Account token, or
			// garbage. Fall back to reading the subject for display only,
			// exactly as before: ParseUnverified's result never authorises
			// anything.
			var claims sjwt.BAASTokenClaims
			if err := sjwt.ParseUnverified(idToken, &claims); err == nil {
				naID = claims.Subject
			} else {
				s.log.Warn("baas: unparseable Nintendo Account id token in /federation", "error", err)
			}
		}
	}
	s.completeLogin(w, r, form, naID, verifiedNAID)
}

func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, form form, nintendoAccountID, attachToUserID string) {
	deviceAccountID := strings.ToLower(form.Get("id"))
	password := form.Get("password")
	if deviceAccountID == "" || password == "" {
		writeError(w, r, errInvalidParams)
		return
	}

	// Checked once the request is otherwise well-formed, so a malformed
	// request does not burn budget on a real attempt; before the store lookup
	// and password compare, so a throttled caller costs neither.
	//
	// Keyed by address AND device account. A console signs in every user
	// profile it holds at boot, and a profile never linked to GRID0+ is
	// refused each time; keyed by address alone, a household's unlinked
	// profiles used up the window and the linked one's game then got 429
	// (seen on hardware as 2124-7001). Guessing one account's password is
	// still capped per address; an unknown id has no password to guess.
	if !s.loginLimiter.Allow(service.ClientIP(r)+"/"+deviceAccountID, s.now()) {
		s.log.Warn("baas: login rate limit exceeded", "client", service.ClientIP(r))
		writeRateLimited(w, s.cfg.LoginWindow)
		return
	}

	da, err := s.store.GetDeviceAccount(r.Context(), deviceAccountID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		// The console presented a device account this server has never issued.
		// That is a real situation — a console that was pointed at a different
		// server, or whose account was removed — and the honest answer is that
		// the credential is invalid. Anything else means guessing who is calling.
		s.log.Warn("baas: login for unknown device account", "device_account", deviceAccountID)
		writeError(w, r, errInvalidDeviceAccount)
		return
	case err != nil:
		writeInternalError(s.log, w, r, "load device account", err)
		return
	}

	if da.DeletedAt != nil {
		writeError(w, r, errUnavailableDeviceAccount)
		return
	}

	// Constant-time comparison. The hashes are the same length every time, so a
	// timing difference here would leak how much of a guess was correct.
	if subtle.ConstantTimeCompare([]byte(hashPassword(password)), []byte(da.PasswordSHA256)) != 1 {
		s.log.Warn("baas: login with wrong password", "device_account", deviceAccountID)
		writeError(w, r, errInvalidDeviceAccount)
		return
	}

	s.loginLimiter.Succeeded(service.ClientIP(r) + "/" + deviceAccountID)

	// Only past this point: the caller has just proven it holds this device
	// account's password. attachToUserID (only ever set by federation, only
	// ever from a GRID0+-verified id token) says which existing person
	// this device account should belong to instead of whoever registerUser
	// gave it moments ago. This never runs on the strength of the id token
	// alone — the password check above is what makes reassigning a
	// credential someone did not prove they hold impossible.
	if attachToUserID != "" && attachToUserID != da.UserID {
		target, err := s.store.GetUser(r.Context(), attachToUserID)
		switch {
		case errors.Is(err, db.ErrNotFound):
			s.log.Warn("baas: federation named a Nintendo Account id that is not a real user",
				"device_account", deviceAccountID, "attach_to", attachToUserID)
		case err != nil:
			writeInternalError(s.log, w, r, "load Nintendo Account link target", err)
			return
		case target.Deleted:
			s.log.Warn("baas: federation named a deleted user",
				"device_account", deviceAccountID, "attach_to", attachToUserID)
		default:
			if err := s.store.ReassignDeviceAccount(r.Context(), da.ID, attachToUserID); err != nil {
				writeInternalError(s.log, w, r, "reassign device account", err)
				return
			}
			s.log.Info("baas: attached device account to an existing person",
				"device_account", da.ID, "from_user", da.UserID, "to_user", attachToUserID)
			da.UserID = attachToUserID
		}
	}

	s.issueLoginTokens(w, r, form, da, nintendoAccountID)
}

// issueLoginTokens finishes a login once a device account's password has
// already been proven: signs the access and id tokens and writes the
// response. Shared by every path that gets here through a different
// credential check — completeLogin's device-account-id+password (the real
// protocol) and loginByUsername's username+password (GRID0+'s own,
// for a person logging an emulator in directly) — so the actual token
// shape only exists in one place.
func (s *Server) issueLoginTokens(w http.ResponseWriter, r *http.Request, form form, da db.DeviceAccount, nintendoAccountID string) {
	user, err := s.store.GetUser(r.Context(), da.UserID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		// The device account points at a user that no longer exists. The row is
		// inconsistent; say so in the log and refuse rather than inventing a user.
		s.log.Error("baas: device account points at a missing user",
			"device_account", da.ID, "user", da.UserID)
		writeError(w, r, errInvalidDeviceAccount)
		return
	case err != nil:
		writeInternalError(s.log, w, r, "load user", err)
		return
	}
	if user.Deleted {
		writeError(w, r, errDeletedUser)
		return
	}

	// An application token is optional. When present it tells us which game is
	// logging in, and its details are copied into the id token so a game server
	// can see them.
	var appInfo *sjwt.AppInfo
	if appToken := form.Get("appAuthNToken"); appToken != "" {
		var claims sjwt.AppTokenClaims
		if err := s.appVerifier.Verify(appToken, &claims); err != nil {
			s.log.Warn("baas: rejected application token at /login", "error", err)
			writeError(w, r, errInvalidAppAuthNToken)
			return
		}
		info := claims.Nintendo
		appInfo = &info
	}

	if nintendoAccountID != "" {
		if err := s.recordNintendoAccountLink(r.Context(), &user, nintendoAccountID, form.Get("naCountry")); err != nil {
			writeInternalError(s.log, w, r, "record Nintendo Account link", err)
			return
		}
	}

	// The console checks this response against ids it cached from earlier responses in
	// the same session, and refuses the login -- with a generic message the server never
	// sees a request for -- when they disagree: a user id that is not the one it logged in
	// with, or a links.nintendoAccount.id that is missing, or is not the NA account id it
	// holds. Logging the three ids is what makes a failed session diagnosable from the
	// server side at all, since a rejection at that gate is followed by no further request
	// to identify it by.
	s.log.Info("baas: login issued tokens",
		"user", user.ID, "device_account", da.ID, "nintendo_account", nintendoAccountID)

	now := s.now()

	accessToken, err := s.accessIssuer.Sign(sjwt.BAASTokenClaims{
		Subject:         user.ID,
		Audience:        s.cfg.Audience,
		IssuedAt:        now.Unix(),
		Expires:         now.Add(s.cfg.AccessTokenTTL).Unix(),
		Issuer:          s.cfg.Issuer,
		TokenID:         sjwt.NewTokenID(),
		Type:            "token",
		GrantType:       2,
		DeviceAccountID: da.ID,
		Status:          sjwt.UserTokenStatus,
	})
	if err != nil {
		writeInternalError(s.log, w, r, "sign user access token", err)
		return
	}

	idClaims := sjwt.BAASTokenClaims{
		Subject:         user.ID,
		Audience:        s.cfg.Audience,
		IssuedAt:        now.Unix(),
		Expires:         now.Add(s.cfg.IDTokenTTL).Unix(),
		Issuer:          s.cfg.Issuer,
		TokenID:         sjwt.NewTokenID(),
		Type:            "id_token",
		DeviceAccountID: da.ID,
	}

	var summary *loginSummary
	if appInfo != nil {
		info := *appInfo
		info.AppTime = now.Unix()
		// A game server that sees GAME_SERVER as the policy handler decides
		// whether online play is allowed from 'hm'. On a private network every
		// registered user is entitled to play, so it is always true.
		if info.PolicyHandler == "GAME_SERVER" {
			hasMembership := true
			info.HasMembership = &hasMembership
			summary = &loginSummary{Nintendo: loginSummaryNintendo{HasMembership: true}}
		}
		idClaims.Nintendo = info
	}

	idToken, err := s.idIssuer.Sign(idClaims)
	if err != nil {
		writeInternalError(s.log, w, r, "sign id token", err)
		return
	}

	// Logging in is an explicit statement that the console is awake, but not
	// that it is playing anything: the game has not started yet. ONLINE is the
	// honest state, and a later presence report upgrades it to PLAYING.
	s.markOnline(r.Context(), user.ID, now)

	das, err := s.store.ListDeviceAccounts(r.Context(), user.ID)
	if err != nil {
		writeInternalError(s.log, w, r, "list device accounts", err)
		return
	}
	fc := s.friendCodeOrNil(r.Context(), user.ID)
	pres, err := s.effectivePresence(r.Context(), user.ID)
	if err != nil {
		writeInternalError(s.log, w, r, "load presence", err)
		return
	}

	appID := ""
	if appInfo != nil {
		appID = appInfo.AppID
	}
	s.log.Info("baas: login", "user", user.ID, "device_account", da.ID, "title", appID)

	thumb1, thumb2 := s.defaultAvatarURLOrEmpty()
	writeJSON(w, http.StatusOK, loginResponse{
		ExpiresIn:   int(s.cfg.AccessTokenTTL.Seconds()),
		User:        userResponse(s.now(), user, fc, pres, das, visSelf, thumb1, thumb2),
		IDToken:     idToken,
		AccessToken: accessToken,
		TokenType:   "Bearer",
		Summary:     summary,
	})
}

// recordNintendoAccountLink stores a Nintendo Account id against a user.
//
// It lives in the 'self' extras rather than in a column because GRID0+ does nothing
// with it: it exists so the console's "linked account" screen has something to show.
//
// Also backfills Birthday/Country if either is still BAAS's own "unset" sentinel
// ("0000-00-00", ""). Those are the right defaults for an anonymous device profile
// (registerUser, provision.go), which is what every user is until this function runs,
// but the very response this call is part of embeds the full user object
// (userResponse, in loginResponse.User), and a console that just did a real Nintendo
// Account federation is looking at a profile that claims to belong to a real, named
// person. Backfilling real-looking values is more correct than an anonymous device
// profile's placeholders for a person who just proved a real Nintendo Account, though
// it is not what makes federation succeed.
//
// naCountryUnknown is the literal string the console's federation request falls back
// to for the naCountry form field when it has nothing real to report: the
// "%s"-formatted value defaults to "Unknown" whenever a console-side flag (whether this
// profile has a previously linked Nintendo Account) is unset, which is always the case
// on a first-time link. It is treated identically to an absent naCountry below, or a real
// console's first link would permanently store "Unknown" as the country: not a valid ISO
// code, and never corrected afterward since recordNintendoAccountLink only backfills an
// empty value.
const naCountryUnknown = "Unknown"

// naCountry is the console's own hint (the request's naCountry form field)
// for which country to backfill, used in preference to a hardcoded one.
func (s *Server) recordNintendoAccountLink(ctx context.Context, user *db.User, naID, naCountry string) error {
	patch := db.UserPatch{}

	extras := user.Extras
	if extras == nil {
		extras = db.EmptyExtras()
	}
	if extras[db.ScopeSelf] == nil {
		extras[db.ScopeSelf] = map[string]any{}
	}
	if existing, ok := extras[db.ScopeSelf]["nintendoAccountId"].(string); !ok || existing != naID {
		extras[db.ScopeSelf]["nintendoAccountId"] = naID
		patch.Extras = &extras
	}
	// UNCONFIRMED: what nxAccountBlob's context-keyed head bytes *mean*. Not
	// whether they matter: the head is rendered from the console's own profile
	// cache and that cache is filled from the last response, so a working server is
	// choosing these bytes too and the client can only ever echo its own copy of
	// them -- see nxAccountBlob's comment. What is left is a server-side choice no
	// capture of this client can decode.
	//
	// Set only once, never overwritten on a later call: a real console PATCHes its
	// own version of this blob back to /1.0.0/users/{id} after the first profile
	// change (patchUser's /extras/self/nxAccount path already stores that
	// correctly). Recomputing and reassigning it here on every subsequent
	// login/federation would stomp whatever the console just pushed back with our
	// own placeholder, and the console keeps retrying to reconcile a value the
	// server keeps undoing.
	if existing, ok := extras[db.ScopeSelf]["nxAccount"].(string); !ok || existing == "" {
		extras[db.ScopeSelf]["nxAccount"] = nxAccountBlob(naID)
		patch.Extras = &extras
	}

	if user.Birthday == "" || user.Birthday == "0000-00-00" {
		birthday := "2000-01-01"
		patch.Birthday = &birthday
	}
	if user.Country == "" || user.Country == naCountryUnknown {
		country := naCountry
		if country == "" || country == naCountryUnknown {
			country = "US"
		}
		patch.Country = &country
	}

	if patch.Extras == nil && patch.Birthday == nil && patch.Country == nil {
		return nil
	}

	updated, err := s.store.UpdateUser(ctx, user.ID, patch)
	if err != nil {
		return err
	}
	*user = updated
	return nil
}

// nxAccountBlob derives extras.self.nxAccount's value: a fixed-length,
// correctly-shaped placeholder, not a copy or a forgery of a real one.
//
// The value is a fixed 174-byte binary structure, base64-encoded to exactly 232
// characters with no padding (174 is divisible by 3). Its layout, as far as GRID0+
// reproduces it:
//
//   - bytes 0-5 are a fixed 6-byte header, the decoded form of the base64 text
//     "01606150". The console checks it before anything else when a federation
//     completes, so it must stay at offset 0, and its content must not change.
//   - bytes 6-21 (payload 0-15) are the varying head. A real console renders them
//     from its own cached profile record. GRID0+ has no such record, so these are
//     derived from naID alone. They are the only range a console insists on: at least
//     one non-zero byte in them, and exactly 168 payload bytes after the header.
//   - bytes 22-29 vary with the call and are three big-endian words rendered from
//     the cached profile; GRID0+ derives them from naID too.
//   - a `01 00 01 01` marker sits at 46-49, and bytes 62-65 and 72-77 hold account
//     state that only login-shaped responses carry. They are left zero here.
//   - bytes 78-173 are a 96-byte zero tail.
//
// The console has no derivation of its own to compare against: it renders whatever the
// last response told it to cache, which is why a working server gives the same account
// two different heads from federation/login and from a profile read.
//
// This is deliberately not derived from anything Nintendo-secret and not a captured
// value (that belongs to one specific real account and isn't this project's to
// redistribute): just naID, which is GRID0+'s own data, hashed into the bytes the head
// needs. It is stable per account, so it doesn't change on every login, but it is a
// placeholder shape, never a claim about what the real bytes encode. Don't move the
// header off offset 0 or change its content without a hardware confirmation first: an
// earlier attempt to do so regressed federation to 2124-0292.
//
// UNCONFIRMED: what the context-keyed head bytes encode, and whether anything else
// reads any of this.
func nxAccountBlob(naID string) string {
	const blobLen = 174

	// Confirmed working on hardware; do not move off offset 0 or change its
	// content without a fresh hardware confirmation -- see the regression
	// this survived, above.
	header := []byte{0xd3, 0x5e, 0xb4, 0xeb, 0x5e, 0x74}

	out := make([]byte, blobLen)
	copy(out, header)

	// Bytes 6-21 -- payload 0-15 -- the varying head. A real console renders these
	// from its own cached profile record; GRID0+ has no such record, so this stands in
	// for it, derived from naID alone. It is also the only range a console insists on:
	// these 16 payload bytes must not be all zero.
	seed := sha256.Sum256([]byte("nxAccount:" + naID))
	copy(out[6:22], seed[:16])

	// Byte 12 is the one byte inside that varying head that is identical in
	// every value of this field either account has ever produced -- 0x10 --
	// while the bytes on both sides of it differ, so it is structure and not
	// data. It is inside the range the console requires non-zero, so writing
	// it changes nothing about that check.
	out[12] = 0x10

	// Bytes 22-29 -- payload 16-23 -- are the third of the builder's three
	// big-endian u64s, and the only one with a shape known accounts agree on: its
	// HIGH half (blob bytes 22-25) is zero and its low half (26-29) is not, i.e. the
	// value is below 2^32 in every sample. Writing 24 hash bytes across 6-29 would put
	// entropy in 22-25 instead, which is a value no real sample has. Zero bytes
	// 22-25 come free from make(); only the low half is written.
	v := binary.BigEndian.Uint32(seed[16:20])
	if v == 0 {
		// Not reachable in practice; kept so the "non-zero" reading stays
		// true of the code and not just of the hash.
		v = 1
	}
	binary.BigEndian.PutUint32(out[26:30], v)

	// Bytes 30-45 -- payload 24-39 -- are the two zero quads the builder
	// writes itself, and stay zero here (make() already left them so).

	// Bytes 46-49 -- payload 40-43 -- the marker every sample carries. It is the one
	// part of the copied profile region identical between a login-shaped and a
	// profile-shaped response, which makes it structure rather than that account's
	// own data.
	copy(out[46:50], []byte{0x01, 0x00, 0x01, 0x01})

	// Bytes 50-173 -- the rest of the copied profile region -- stay zero;
	// see the comment above for why an empty record is the honest value.

	return base64.StdEncoding.EncodeToString(out)
}

// ---- GET /1.0.0/users/<id> ----------------------------------------------

// getUser returns a user's own record.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#get-100usersid
//
// Nintendo answers insufficient_scope if the token does not belong to the user
// in the path, and so does GRID0+: this endpoint is for reading yourself.
func (s *Server) getUser(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}

	user, err := s.store.GetUser(r.Context(), c.UserID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, r, errResourceNotFound)
		return
	case err != nil:
		writeInternalError(s.log, w, r, "load user", err)
		return
	}

	das, err := s.store.ListDeviceAccounts(r.Context(), c.UserID)
	if err != nil {
		writeInternalError(s.log, w, r, "list device accounts", err)
		return
	}
	pres, err := s.effectivePresence(r.Context(), c.UserID)
	if err != nil {
		writeInternalError(s.log, w, r, "load presence", err)
		return
	}

	thumb1, thumb2 := s.defaultAvatarURLOrEmpty()
	writeJSON(w, http.StatusOK,
		userResponse(s.now(), user, s.friendCodeOrNil(r.Context(), c.UserID), pres, das, visSelf, thumb1, thumb2))
}

// ---- GET /1.0.0/users ---------------------------------------------------

// findUsers looks a user up by friend code or nickname.
//
// The console's real query parameter for a friend code is a MongoDB-style filter:
// "filter.links.friendCode.id.$in=<code>", not the plain "friendCode=<code>" this
// endpoint originally guessed. "friendCode" is kept as an accepted alias, since it is
// cheap to support and tooling may already use it, but the real console sends the
// filter form.
//
//	GET /1.0.0/users?filter.links.friendCode.id.$in=7779-8151-4794  (real)
//	GET /1.0.0/users?filter.id.$in=<16hex>[,<16hex>...]             (real)
//	GET /1.0.0/users?friendCode=7779-8151-4794                      (alias)
//	GET /1.0.0/users?nickname=someone
//	GET /1.0.0/users?ids=aaaa...,bbbb...
//
// and returns an envelope of stranger-visibility user objects.
//
// For the id form, the console de-duplicates the id list and builds
// "https://%s/1.0.0/users?filter.id.$in=" followed by "%016llx" per id with a ','
// between them, so the ids are comma-separated 16-hex-digit strings (an endpoint that
// refuses these as unfiltered fails the whole call). The response is read as
// $.items[*], $.items[*].id, .nickname, .thumbnailUrl and .extras.*.playLog: an
// envelope, like every other list here.
//
// UNCONFIRMED: whether nickname also goes through the filter.* convention rather
// than its own plain query key; only the friend-code and id forms are known.
func (s *Server) findUsers(w http.ResponseWriter, r *http.Request, c *caller) {
	friendCode := r.URL.Query().Get("filter.links.friendCode.id.$in")
	if friendCode == "" {
		friendCode = r.URL.Query().Get("friendCode")
	}
	q := db.UserQuery{
		FriendCode: normaliseFriendCode(friendCode),
		Nickname:   r.URL.Query().Get("nickname"),
		Limit:      50,
	}
	// The console's real id lookup: "filter.id.$in=<id>,<id>,...". "ids" stays as
	// this server's own alias.
	idsParam := r.URL.Query().Get("filter.id.$in")
	if idsParam == "" {
		idsParam = r.URL.Query().Get("ids")
	}
	if idsParam != "" {
		for _, id := range strings.Split(idsParam, ",") {
			if id = strings.ToLower(strings.TrimSpace(id)); id != "" {
				q.IDs = append(q.IDs, id)
			}
		}
	}
	if q.FriendCode == "" && q.Nickname == "" && len(q.IDs) == 0 {
		// An unfiltered query would enumerate every account on the server to
		// any authenticated console. Refuse rather than leak the member list.
		writeError(w, r, errInvalidParams)
		return
	}

	users, err := s.store.FindUsers(r.Context(), q)
	if err != nil {
		writeInternalError(s.log, w, r, "find users", err)
		return
	}

	defaultThumb1, defaultThumb2 := s.defaultAvatarURLOrEmpty()
	out := make([]UserResponse, 0, len(users))
	for _, u := range users {
		if u.ID == c.UserID {
			// Finding yourself by your own friend code is not useful and, if the
			// console acts on it, produces a self-friend-request.
			continue
		}
		blocked, err := s.store.IsBlockedEitherWay(r.Context(), c.UserID, u.ID)
		if err != nil {
			writeInternalError(s.log, w, r, "check blocks", err)
			return
		}
		if blocked {
			// A blocked user is not findable, in either direction. Returning
			// them would let a blocked person confirm the account still exists.
			continue
		}
		pres, err := s.effectivePresence(r.Context(), u.ID)
		if err != nil {
			writeInternalError(s.log, w, r, "load presence", err)
			return
		}
		out = append(out, userResponse(s.now(), u, s.friendCodeOrNil(r.Context(), u.ID), pres, nil, visStranger, defaultThumb1, defaultThumb2))
	}

	// An envelope, not a bare array: both of the console's parsers for this
	// endpoint read $.items[*] and match nothing at the top level. See the
	// comment above.
	writeJSON(w, http.StatusOK, newCollection(q.Limit, out))
}

// ---- device accounts ----------------------------------------------------

// deleteDeviceAccount removes a device account.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#delete-100usersiddevice_accountsid
func (s *Server) deleteDeviceAccount(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}
	daid := strings.ToLower(r.PathValue("daid"))

	err := s.store.DeleteDeviceAccount(r.Context(), c.UserID, daid)
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, r, errResourceNotFound)
		return
	case err != nil:
		writeInternalError(s.log, w, r, "delete device account", err)
		return
	}

	s.log.Info("baas: deleted device account", "user", c.UserID, "device_account", daid)
	w.WriteHeader(http.StatusNoContent)
}

// ---- account linking ---------------------------------------------------

// linkAccount records a Nintendo Account against a user.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#post-100usersidlink
//
// As with /1.0.0/federation, the id token cannot be verified by GRID0+ and is
// treated as a label. Linking grants nothing.
func (s *Server) linkAccount(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}
	form, err := parseForm(r)
	if err != nil {
		writeError(w, r, errInvalidParams)
		return
	}
	if form.Get("idp") != "nintendoAccount" {
		writeError(w, r, errInvalidParams)
		return
	}

	var naID string
	if idToken := form.Get("idToken"); idToken != "" {
		var claims sjwt.BAASTokenClaims
		if err := sjwt.ParseUnverified(idToken, &claims); err == nil {
			naID = claims.Subject
		}
	}
	if naID == "" {
		writeError(w, r, errInvalidParams)
		return
	}

	user, err := s.store.GetUser(r.Context(), c.UserID)
	if err != nil {
		writeInternalError(s.log, w, r, "load user", err)
		return
	}
	if err := s.recordNintendoAccountLink(r.Context(), &user, naID, form.Get("naCountry")); err != nil {
		writeInternalError(s.log, w, r, "record Nintendo Account link", err)
		return
	}
	s.writeSelf(w, r, user)
}

// unlinkAccount removes the recorded Nintendo Account.
func (s *Server) unlinkAccount(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}

	user, err := s.store.GetUser(r.Context(), c.UserID)
	if err != nil {
		writeInternalError(s.log, w, r, "load user", err)
		return
	}
	extras := user.Extras
	if extras[db.ScopeSelf] != nil {
		delete(extras[db.ScopeSelf], "nintendoAccountId")
	}
	user, err = s.store.UpdateUser(r.Context(), c.UserID, db.UserPatch{Extras: &extras})
	if err != nil {
		writeInternalError(s.log, w, r, "update user", err)
		return
	}
	s.writeSelf(w, r, user)
}

// writeSelf sends a full self-visibility user object.
func (s *Server) writeSelf(w http.ResponseWriter, r *http.Request, user db.User) {
	das, err := s.store.ListDeviceAccounts(r.Context(), user.ID)
	if err != nil {
		writeInternalError(s.log, w, r, "list device accounts", err)
		return
	}
	pres, err := s.effectivePresence(r.Context(), user.ID)
	if err != nil {
		writeInternalError(s.log, w, r, "load presence", err)
		return
	}
	thumb1, thumb2 := s.defaultAvatarURLOrEmpty()
	writeJSON(w, http.StatusOK,
		userResponse(s.now(), user, s.friendCodeOrNil(r.Context(), user.ID), pres, das, visSelf, thumb1, thumb2))
}

// friendCodeOrNil returns a user's friend code, or nil if they have none.
//
// A missing friend code is normal: one is only created when the console asks for
// it. A lookup error is logged and treated as "no code", because failing a
// friend-list request over a missing decoration would be worse than the
// decoration being absent.
func (s *Server) friendCodeOrNil(ctx context.Context, userID string) *db.FriendCode {
	fc, err := s.store.GetFriendCode(ctx, userID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return nil
	case err != nil:
		s.log.Warn("baas: load friend code", "user", userID, "error", err)
		return nil
	}
	return &fc
}

// markOnline records that a user is online but not in a game.
func (s *Server) markOnline(ctx context.Context, userID string, at time.Time) {
	if err := s.store.TouchPresence(ctx, userID, at); err != nil {
		s.log.Warn("baas: touch presence", "user", userID, "error", err)
	}
}
