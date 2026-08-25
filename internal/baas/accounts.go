package baas

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
	sjwt "github.com/n-popescu/switchnet/internal/jwt"
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
	}, nil, visSelf)
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
	s.completeLogin(w, r, form, "")
}

// federation is /1.0.0/login plus a Nintendo Account id token.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#post-100federation
//
// SwitchNet does not host accounts.nintendo.com, so the id token in this request
// came from somewhere it cannot verify. It is therefore treated as a label, not
// as a credential: its subject is recorded as a linked account id and it grants
// nothing. Authentication is still the device account id and password, exactly
// as in /1.0.0/login.
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

	// Read the subject for display only. ParseUnverified is used deliberately
	// and its result never authorises anything.
	var naID string
	if idToken := form.Get("idToken"); idToken != "" {
		var claims sjwt.BAASTokenClaims
		if err := sjwt.ParseUnverified(idToken, &claims); err == nil {
			naID = claims.Subject
		} else {
			s.log.Warn("baas: unparseable Nintendo Account id token in /federation", "error", err)
		}
	}
	s.completeLogin(w, r, form, naID)
}

func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, form form, nintendoAccountID string) {
	deviceAccountID := strings.ToLower(form.Get("id"))
	password := form.Get("password")
	if deviceAccountID == "" || password == "" {
		writeError(w, r, errInvalidParams)
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
		if err := s.recordNintendoAccountLink(r.Context(), &user, nintendoAccountID); err != nil {
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
		User:        userResponse(s.now(), user, fc, pres, das, visSelf),
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
func (s *Server) recordNintendoAccountLink(ctx context.Context, user *db.User, naID string) error {
	extras := user.Extras
	if extras == nil {
		extras = db.EmptyExtras()
	}
	if extras[db.ScopeSelf] == nil {
		extras[db.ScopeSelf] = map[string]any{}
	}
	if existing, ok := extras[db.ScopeSelf]["nintendoAccountId"].(string); ok && existing == naID {
		return nil
	}
	extras[db.ScopeSelf]["nintendoAccountId"] = naID

	updated, err := s.store.UpdateUser(ctx, user.ID, db.UserPatch{Extras: &extras})
	if err != nil {
		return err
	}
	*user = updated
	return nil
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
		userResponse(s.now(), user, s.friendCodeOrNil(r.Context(), c.UserID), pres, das, visSelf))
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
		out = append(out, userResponse(s.now(), u, s.friendCodeOrNil(r.Context(), u.ID), pres, nil, visStranger))
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
	if err := s.recordNintendoAccountLink(r.Context(), &user, naID); err != nil {
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
		userResponse(s.now(), user, s.friendCodeOrNil(r.Context(), user.ID), pres, das, visSelf))
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
