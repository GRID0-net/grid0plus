package baas

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
	"github.com/n-popescu/switchnet/internal/service"
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
// access token. The device token it presents was issued by SwitchNet's own dauth
// server, so verifying it checks SwitchNet's signature — it is not a claim about
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
			// Echoed from the device token unchanged, matching a 2026-08-27
			// capture of a working Nextendo application/token response. See
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
		ID:               userID,
		Birthday:         "0000-00-00",
		Permissions:      db.DefaultPermissions(now),
		Extras:           db.EmptyExtras(),
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

	resp := userResponse(s.now(), user, nil, db.Presence{
		UserID: userID, State: db.StateOffline, Extras: db.EmptyExtras(), ReportedAt: now,
	}, nil, visSelf, s.defaultAvatarURLOrEmpty())
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
// This is where identity is decided, and it is the one function in SwitchNet
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

// federation is /1.0.0/login plus a Nintendo Account id token.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#post-100federation
//
// A REAL Nintendo Account id token cannot be verified here — SwitchNet does
// not have Nintendo's key — so in general this is treated as a label, not a
// credential: its subject is recorded as a linked account id (see
// recordNintendoAccountLink) and grants nothing on its own. Authentication is
// still the device account id and password, exactly as in /1.0.0/login.
//
// One case is different: a token that verifies against SwitchNet's own
// idIssuer key can only exist because nalink.go minted it, for the person who
// just signed in on the Nintendo Account link page. Its subject is a real,
// existing SwitchNet user id — not an opaque label — and this is the only
// place that reassigns the device account /1.0.0/users just created to that
// person, rather than leaving the console permanently attached to the fresh,
// empty, unreachable-by-username identity registerUser had to invent because
// it runs before any id token exists. See completeLogin's use of
// idVerifier. Found on real hardware 2026-08-26: without this, the console
// federates successfully, but the "logged in" identity it ends up with is
// not the one whose credentials it just typed into the login page, and it
// gives up soon after (undocumented error 2124-0292) with nothing further
// logged — see docs/CAPTURES for the session write-up.
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
	if !s.loginLimiter.Allow(service.ClientIP(r), s.now()) {
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

	// Only past this point: the caller has just proven it holds this device
	// account's password. attachToUserID (only ever set by federation, only
	// ever from a SwitchNet-verified id token) says which existing person
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

	user, err := s.store.GetUser(r.Context(), da.UserID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		// The device account points at a user that no longer exists. The row is
		// inconsistent; say so in the log and refuse rather than inventing a user.
		s.log.Error("baas: device account points at a missing user",
			"device_account", deviceAccountID, "user", da.UserID)
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

	writeJSON(w, http.StatusOK, loginResponse{
		ExpiresIn:   int(s.cfg.AccessTokenTTL.Seconds()),
		User:        userResponse(s.now(), user, fc, pres, das, visSelf, s.defaultAvatarURLOrEmpty()),
		IDToken:     idToken,
		AccessToken: accessToken,
		TokenType:   "Bearer",
		Summary:     summary,
	})
}

// recordNintendoAccountLink stores a Nintendo Account id against a user.
//
// It lives in the 'self' extras rather than in a column because SwitchNet does
// nothing with it: it exists so the console's "linked account" screen has
// something to show, and so a capture round-trips.
//
// Also backfills Birthday/Country if either is still BAAS's own "unset"
// sentinel ("0000-00-00", ""). Those are the right defaults for an anonymous
// device profile (registerUser, provision.go), which is what every user is
// until this function runs — but the very response this call is part of
// embeds the full user object (userResponse, in loginResponse.User), and a
// console that just did a real Nintendo Account federation is looking at a
// profile that claims to belong to a real, named person. Suspected once
// (2026-08-26) of being the cause of an undocumented error (2124-0292) —
// that suspicion did not hold up: a 2026-08-27 capture of a real, working
// account showed birthday and country left at their own sentinels the whole
// time. Backfilling real-looking values here is kept anyway, since it is
// still more correct than an anonymous device profile's placeholders for a
// person who just proved a real Nintendo Account, but it is not, on its
// own, what makes federation succeed.
//
// naCountry is the console's own hint (the request's naCountry form field)
// for which country to backfill, used in preference to a hardcoded one --
// found 2026-08-27 that this was previously always "US" regardless of what
// the console actually sent.
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
	// TODO(capture-needed): the interior byte layout of nxAccountBlob's
	// output -- what a real one encodes, and whether the console checks any
	// of it. See that function's own comment for what a 2026-09-04 capture
	// settled (length and encoding) versus what it did not (content).
	nxAccount := nxAccountBlob(naID)
	if existing, ok := extras[db.ScopeSelf]["nxAccount"].(string); !ok || existing != nxAccount {
		extras[db.ScopeSelf]["nxAccount"] = nxAccount
		patch.Extras = &extras
	}

	if user.Birthday == "" || user.Birthday == "0000-00-00" {
		birthday := "2000-01-01"
		patch.Birthday = &birthday
	}
	if user.Country == "" {
		country := naCountry
		if country == "" {
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
// Real shape confirmed 2026-09-04, from a genuine, decrypted, working
// Nextendo federation response the operator captured and supplied directly
// (a mitmproxy `.flow`, read with the real `mitmproxy` library rather than
// eyeballed): a fixed 174-byte binary structure, base64-encoded to exactly
// 232 characters with no padding (174 is exactly divisible by 3, so base64
// needs none). That corrects this comment's own earlier, unverified guess
// -- "a non-base64 alphabet" -- written before any real sample existed to
// check it against; decoded, it is ordinary base64 of opaque bytes.
//
// The interior byte layout remains unknown. One real account's value is not
// enough to separate fixed structure from per-account content, and
// CLAUDE.md's rule against inventing a plausible-looking Nintendo internal
// means this does not attempt to reproduce either. What changed from the
// previous placeholder (the bare Nintendo Account id, ~16 characters) is
// only the parts now confirmed real: length and encoding. A 16-character
// hex string sitting where a console has only ever seen a 232-character
// value was a mismatch nothing about "content unconfirmed" excused --
// matching a confirmed length costs nothing and is never a worse guess than
// visibly not even trying to.
//
// Deliberately not derived from anything Nintendo-secret and not the
// captured value itself (that value belongs to one specific real Nintendo
// Account and is not this project's to redistribute, even reshaped): just
// naID, which is SwitchNet's own data, stretched to the confirmed length
// with repeated SHA-256. Stable per account, so it does not change on every
// login, but it is a placeholder shape, never a claim about what the real
// bytes encode.
//
// TODO(capture-needed): whether the interior bytes matter to the console at
// all. One data point exists and is suggestive but not conclusive: on real
// hardware, the operator tried both another real account's literal captured
// bytes and unformatted random data in place of this field, in separate
// live federation attempts -- both still produced error 2124-0292, the same
// error this whole flow has shown through several previous, independently
// capture-verified fixes (see docs/CAPTURES/nintendo-account-link.md's
// "What is still open"). That is consistent with the console never checking
// this field's content at all, but a content check keyed to per-request
// context (so that even genuine bytes from a different request would also
// read as wrong) predicts the identical result -- so it does not settle the
// question either way. Change: this function, if a capture or a hardware
// test ever ties specific interior bytes to specific console behaviour.
func nxAccountBlob(naID string) string {
	const blobLen = 174
	out := make([]byte, 0, blobLen)
	seed := []byte(naID)
	for len(out) < blobLen {
		sum := sha256.Sum256(seed)
		out = append(out, sum[:]...)
		seed = sum[:]
	}
	return base64.StdEncoding.EncodeToString(out[:blobLen])
}

// ---- GET /1.0.0/users/<id> ----------------------------------------------

// getUser returns a user's own record.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#get-100usersid
//
// Nintendo answers insufficient_scope if the token does not belong to the user
// in the path, and so does SwitchNet: this endpoint is for reading yourself.
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

	writeJSON(w, http.StatusOK,
		userResponse(s.now(), user, s.friendCodeOrNil(r.Context(), c.UserID), pres, das, visSelf, s.defaultAvatarURLOrEmpty()))
}

// ---- GET /1.0.0/users ---------------------------------------------------

// findUsers looks a user up by friend code or nickname.
//
// TODO(capture-needed): the wiki lists GET /1.0.0/users as a friends-module
// endpoint but documents neither its parameters nor its response. The friends
// applet has to turn a typed-in friend code into a user somehow, and this is the
// only endpoint that can plausibly do it, so SwitchNet accepts:
//
//	GET /1.0.0/users?friendCode=7779-8151-4794
//	GET /1.0.0/users?nickname=someone
//	GET /1.0.0/users?ids=aaaa...,bbbb...
//
// and returns a JSON array of stranger-visibility user objects.
//
// Capture with: type a friend code into "Add Friend" on a real console and read
// the request the friends sysmodule makes. If it turns out the code is resolved
// inside POST /2.0.0/friend_requests instead, that path already accepts a
// friendCode parameter and this endpoint can stay as a convenience for tooling.
func (s *Server) findUsers(w http.ResponseWriter, r *http.Request, c *caller) {
	q := db.UserQuery{
		FriendCode: normaliseFriendCode(r.URL.Query().Get("friendCode")),
		Nickname:   r.URL.Query().Get("nickname"),
		Limit:      50,
	}
	if ids := r.URL.Query().Get("ids"); ids != "" {
		q.IDs = strings.Split(ids, ",")
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

	defaultThumbnail := s.defaultAvatarURLOrEmpty()
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
		out = append(out, userResponse(s.now(), u, s.friendCodeOrNil(r.Context(), u.ID), pres, nil, visStranger, defaultThumbnail))
	}
	writeJSON(w, http.StatusOK, out)
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
// As with /1.0.0/federation, the id token cannot be verified by SwitchNet and is
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
	writeJSON(w, http.StatusOK,
		userResponse(s.now(), user, s.friendCodeOrNil(r.Context(), user.ID), pres, das, visSelf, s.defaultAvatarURLOrEmpty()))
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
