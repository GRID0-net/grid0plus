package baas

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/n-popescu/switchnet/internal/db"
)

// defaultFriendCount is the page size the console asks for, and the cap applied
// when it does not ask.
const defaultFriendCount = 300

// maxFriendCount bounds what a single request can pull back, so a console (or
// something pretending to be one) cannot ask for the whole table at once.
const maxFriendCount = 1000

// defaultFriendRequestCount is the default page size for inbox and outbox.
// Confirmed by the captures at docs/CAPTURES/raw/04-friend-requests-inbox.json
// and 05-friend-requests-outbox.json.
const defaultFriendRequestCount = 50

// ---- GET /2.0.0/users/<id>/friends --------------------------------------

// listFriends returns the caller's friend list with each friend's presence.
//
// Each entry is a relationship object wrapping the friend's profile. The
// response is a CollectionEnvelope, confirmed by the capture at
// docs/CAPTURES/raw/03-friends.json.
func (s *Server) listFriends(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}

	count := defaultFriendCount
	if v := r.URL.Query().Get("count"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, r, errInvalidParams)
			return
		}
		count = min(n, maxFriendCount)
	}

	friendships, err := s.store.ListFriends(r.Context(), c.UserID, count)
	if err != nil {
		writeInternalError(s.log, w, r, "list friends", err)
		return
	}

	ids := make([]string, 0, len(friendships))
	for _, f := range friendships {
		ids = append(ids, f.FriendID)
	}

	users, err := s.store.FindUsers(r.Context(), db.UserQuery{IDs: ids})
	if err != nil {
		writeInternalError(s.log, w, r, "load friends", err)
		return
	}
	byID := make(map[string]db.User, len(users))
	for _, u := range users {
		byID[u.ID] = u
	}

	presences, err := s.effectivePresences(r.Context(), ids)
	if err != nil {
		writeInternalError(s.log, w, r, "load friend presence", err)
		return
	}

	out := make([]FriendResponse, 0, len(friendships))
	for _, f := range friendships {
		user, ok := byID[f.FriendID]
		if !ok {
			// The friendship names a user that no longer exists. Skip it rather
			// than emitting a half-populated entry the applet would draw as a
			// blank row.
			s.log.Warn("baas: friendship names a missing user",
				"user", c.UserID, "friend", f.FriendID)
			continue
		}

		// The friend decides whether the caller may see their presence. This is
		// the friend's own privacy setting, applied from the friend's side of
		// the relationship — not the caller's.
		vis := visFriend
		if f.Favorite {
			vis = visFavoriteFriend
		}
		pres := presences[f.FriendID]
		if !s.presenceVisibleTo(r.Context(), user, c.UserID) {
			pres = db.Presence{
				UserID: user.ID, State: db.StateOffline, Extras: db.EmptyExtras(),
				ReportedAt: pres.ReportedAt,
			}
		}

		out = append(out, FriendResponse{
			UserID:     c.UserID,
			FriendID:   f.FriendID,
			Friend:     friendUserResponse(user, pres, vis),
			IsFavorite: f.Favorite,
			Channels:   map[string]any{},
			Extras:     db.EmptyExtras(),
			FriendNote: "",
			ETag:       "",
			CreatedAt:  f.BecameFriendAt.Unix(),
			UpdatedAt:  f.BecameFriendAt.Unix(),
		})
	}

	writeJSON(w, http.StatusOK, newCollection(count, out))
}

// presenceVisibleTo reports whether viewer may see owner's presence.
//
// The owner's permissions.presence setting decides:
//
//	FRIENDS           any friend (the default)
//	FAVORITE_FRIENDS  only friends the owner marked as favourites
//	SELF              nobody
//
// The default has to be FRIENDS. SELF as a default is a friend list where
// everybody is permanently offline and nothing says why.
func (s *Server) presenceVisibleTo(ctx context.Context, owner db.User, viewerID string) bool {
	switch owner.Permissions.Presence {
	case "SELF":
		return false
	case "FAVORITE_FRIENDS":
		// The owner's own favourite flag, on the owner's side of the friendship.
		f, err := s.store.GetFriendship(ctx, owner.ID, viewerID)
		if err != nil {
			return false
		}
		return f.Favorite
	default:
		// FRIENDS, and anything unrecognised. A setting we do not know must not
		// silently hide presence.
		f, err := s.store.GetFriendship(ctx, owner.ID, viewerID)
		return err == nil && f.UserID != ""
	}
}

// ---- PATCH /2.0.0/users/<id>/friends/<id> -------------------------------

// patchFriend sets the favourite flag on one friend.
//
// The flag is per-direction: A marking B as a favourite says nothing about what
// B sees.
func (s *Server) patchFriend(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}
	other := strings.ToLower(r.PathValue("other"))

	ops, err := parseJSONPatch(w, r)
	if err != nil {
		writeError(w, r, errInvalidParams)
		return
	}

	favorite := false
	seen := false
	for _, op := range ops {
		switch op.Path {
		case "/isFavorite", "/favorite":
			v, ok := op.boolValue()
			if !ok {
				writeError(w, r, errInvalidParams)
				return
			}
			favorite = v
			seen = true
		default:
			s.log.Warn("baas: unsupported friend patch path",
				"user", c.UserID, "friend", other, "path", op.Path)
			writeError(w, r, errInvalidParams)
			return
		}
	}
	if !seen {
		writeError(w, r, errInvalidParams)
		return
	}

	err = s.store.SetFavorite(r.Context(), c.UserID, other, favorite)
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, r, errResourceNotFound)
		return
	case err != nil:
		writeInternalError(s.log, w, r, "set favourite", err)
		return
	}

	s.log.Info("baas: friend favourite changed",
		"user", c.UserID, "friend", other, "favorite", favorite)
	w.WriteHeader(http.StatusNoContent)
}

// ---- DELETE /2.0.0/users/<id>/friends/<id> ------------------------------

// removeFriend unfriends, in both directions.
func (s *Server) removeFriend(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}
	other := strings.ToLower(r.PathValue("other"))

	err := s.store.RemoveFriendship(r.Context(), c.UserID, other)
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, r, errResourceNotFound)
		return
	case err != nil:
		writeInternalError(s.log, w, r, "remove friendship", err)
		return
	}

	s.log.Info("baas: unfriended", "user", c.UserID, "friend", other)
	w.WriteHeader(http.StatusNoContent)
}

// ---- GET /2.0.0/users/<id>/relationships/<id> ---------------------------

// getRelationship reports how the caller relates to another user.
func (s *Server) getRelationship(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}
	other := strings.ToLower(r.PathValue("other"))
	if other == c.UserID {
		writeError(w, r, errInvalidParams)
		return
	}

	user, err := s.store.GetUser(r.Context(), other)
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, r, errResourceNotFound)
		return
	case err != nil:
		writeInternalError(s.log, w, r, "load user", err)
		return
	}

	resp := RelationshipResponse{Type: RelationshipNone}

	blocked, err := s.store.IsBlockedEitherWay(r.Context(), c.UserID, other)
	if err != nil {
		writeInternalError(s.log, w, r, "check blocks", err)
		return
	}

	switch {
	case blocked:
		resp.Type = RelationshipBlocked

	default:
		if f, err := s.store.GetFriendship(r.Context(), c.UserID, other); err == nil {
			resp.Type = RelationshipFriend
			resp.IsFavorite = f.Favorite
			resp.BecameFriendAt = f.BecameFriendAt.Unix()
			break
		}
		if _, err := s.store.FindPendingRequest(r.Context(), c.UserID, other); err == nil {
			resp.Type = RelationshipSent
			break
		}
		if _, err := s.store.FindPendingRequest(r.Context(), other, c.UserID); err == nil {
			resp.Type = RelationshipReceived
		}
	}

	// A blocked user gets no profile back at all: confirming the account exists
	// is exactly what a block is meant to prevent.
	if resp.Type != RelationshipBlocked {
		vis := visStranger
		if resp.Type == RelationshipFriend {
			vis = visFriend
			if resp.IsFavorite {
				vis = visFavoriteFriend
			}
		}
		pres, err := s.effectivePresence(r.Context(), other)
		if err != nil {
			writeInternalError(s.log, w, r, "load presence", err)
			return
		}
		if !s.presenceVisibleTo(r.Context(), user, c.UserID) {
			pres = db.Presence{UserID: other, State: db.StateOffline, Extras: db.EmptyExtras()}
		}
		u := userResponse(s.now(), user, s.friendCodeOrNil(r.Context(), other), pres, nil, vis, s.defaultAvatarURLOrEmpty())
		resp.User = &u
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---- POST /2.0.0/friend_requests ---------------------------------------

// createFriendRequestBody is the request body.
//
// TODO(capture-needed): undocumented. The friends applet can add a friend two
// ways — by typing a friend code, and by picking somebody the console already
// knows about — so both a code and a user id are accepted. Form encoding and
// JSON are both accepted for the same reason: which one the sysmodule uses is
// not documented, and refusing the wrong guess would be a dead end during
// bring-up.
type createFriendRequestBody struct {
	// FriendCode is the twelve-digit code, with or without dashes and with or
	// without the "SW-" prefix.
	FriendCode string `json:"friendCode"`
	// UserID is the target's BAAS user id.
	UserID string `json:"userId"`
	// NSAID is another name the console uses for the same thing.
	NSAID string `json:"nsaId"`
}

// createFriendRequest sends a friend request.
func (s *Server) createFriendRequest(w http.ResponseWriter, r *http.Request, c *caller) {
	body, err := s.parseFriendRequestBody(r)
	if err != nil {
		writeError(w, r, errInvalidParams)
		return
	}

	target, ok := s.resolveTarget(w, r, body)
	if !ok {
		return
	}

	if target.ID == c.UserID {
		// Refusing this is not pedantry. When identity resolution upstream is
		// broken, "add a friend" becomes a self-request surprisingly often, and
		// a friend graph full of self-edges is very hard to read back.
		s.log.Warn("baas: refused self friend request", "user", c.UserID)
		writeError(w, r, errInvalidParams)
		return
	}

	blocked, err := s.store.IsBlockedEitherWay(r.Context(), c.UserID, target.ID)
	if err != nil {
		writeInternalError(s.log, w, r, "check blocks", err)
		return
	}
	if blocked {
		// Deliberately the same error as "does not accept requests", so a
		// blocked sender cannot tell a block apart from a preference.
		writeError(w, r, errForbidden)
		return
	}

	if !target.Permissions.FriendRequestReception {
		writeError(w, r, errForbidden)
		return
	}

	if _, err := s.store.GetFriendship(r.Context(), c.UserID, target.ID); err == nil {
		writeError(w, r, errResourceAlreadyExists)
		return
	}

	// If the target already asked us, accept instead of queueing a second
	// request in the other direction. Two people who add each other at the same
	// moment should end up friends, not with an unanswered request each.
	if incoming, err := s.store.FindPendingRequest(r.Context(), target.ID, c.UserID); err == nil {
		if err := s.acceptRequest(r.Context(), incoming); err != nil {
			writeInternalError(s.log, w, r, "accept crossing friend request", err)
			return
		}
		s.log.Info("baas: friend requests crossed, became friends",
			"user", c.UserID, "friend", target.ID)
		writeJSON(w, http.StatusCreated, FriendRequestResponse{
			ID:        incoming.ID,
			State:     db.RequestAccepted,
			CreatedAt: incoming.CreatedAt.Unix(),
			UpdatedAt: s.now().Unix(),
		})
		return
	}

	now := s.now()
	req, err := s.store.CreateFriendRequest(r.Context(), db.FriendRequest{
		ID:         uuid.NewString(),
		FromUserID: c.UserID,
		ToUserID:   target.ID,
		State:      db.RequestPending,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	if err != nil {
		writeInternalError(s.log, w, r, "create friend request", err)
		return
	}

	s.log.Info("baas: friend request sent", "from", c.UserID, "to", target.ID, "request", req.ID)

	writeJSON(w, http.StatusCreated, s.friendRequestResponse(r.Context(), req, c.UserID))
}

func (s *Server) parseFriendRequestBody(r *http.Request) (createFriendRequestBody, error) {
	var body createFriendRequestBody

	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	if strings.TrimSpace(ct) == "application/json" {
		if err := decodeJSONBody(r, &body); err != nil {
			return body, err
		}
		return body, nil
	}

	f, err := parseForm(r)
	if err != nil {
		return body, err
	}
	body.FriendCode = f.Get("friendCode")
	body.UserID = f.Get("userId")
	body.NSAID = f.Get("nsaId")
	return body, nil
}

// resolveTarget turns a friend code or user id into a user.
func (s *Server) resolveTarget(w http.ResponseWriter, r *http.Request, body createFriendRequestBody) (db.User, bool) {
	if code := normaliseFriendCode(body.FriendCode); code != "" {
		users, err := s.store.FindUsers(r.Context(), db.UserQuery{FriendCode: code, Limit: 1})
		if err != nil {
			writeInternalError(s.log, w, r, "look up friend code", err)
			return db.User{}, false
		}
		if len(users) == 0 {
			// Nintendo has a dedicated error for a malformed code. An unknown
			// but well-formed code is simply not found.
			writeError(w, r, errResourceNotFound)
			return db.User{}, false
		}
		return users[0], true
	}
	if body.FriendCode != "" {
		// Something was supplied that does not look like a friend code at all.
		writeError(w, r, errInvalidFriendCodeFormat)
		return db.User{}, false
	}

	id := strings.ToLower(body.UserID)
	if id == "" {
		id = strings.ToLower(body.NSAID)
	}
	if len(id) != 16 {
		writeError(w, r, errInvalidParams)
		return db.User{}, false
	}

	user, err := s.store.GetUser(r.Context(), id)
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, r, errResourceNotFound)
		return db.User{}, false
	case err != nil:
		writeInternalError(s.log, w, r, "load user", err)
		return db.User{}, false
	}
	if user.Deleted {
		writeError(w, r, errDeletedUser)
		return db.User{}, false
	}
	return user, true
}

// ---- PATCH /2.0.0/friend_requests/<id> ---------------------------------

// resolveFriendRequest accepts, denies or cancels a request.
//
// TODO(capture-needed): the body is undocumented. Both a JSON patch on /state
// and a plain {"state":"..."} object are accepted, because either is a plausible
// shape for a PATCH and guessing wrong would stop friend requests working with
// nothing in the log to explain it.
func (s *Server) resolveFriendRequest(w http.ResponseWriter, r *http.Request, c *caller) {
	rid := r.PathValue("rid")

	req, err := s.store.GetFriendRequest(r.Context(), rid)
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, r, errResourceNotFound)
		return
	case err != nil:
		writeInternalError(s.log, w, r, "load friend request", err)
		return
	}

	// Only the two parties may touch a request, and each may only do their own
	// half: the recipient accepts or denies, the sender cancels.
	if c.UserID != req.FromUserID && c.UserID != req.ToUserID {
		s.log.Warn("baas: refused friend request action by a third party",
			"caller", c.UserID, "request", rid)
		writeError(w, r, errInsufficientScope)
		return
	}
	if req.State != db.RequestPending {
		writeError(w, r, errResourceAlreadyExists)
		return
	}

	state, err := parseRequestState(r)
	if err != nil {
		writeError(w, r, errInvalidParams)
		return
	}

	isRecipient := c.UserID == req.ToUserID
	switch state {
	case db.RequestAccepted, db.RequestDenied:
		if !isRecipient {
			writeError(w, r, errInsufficientScope)
			return
		}
	case db.RequestCancelled:
		if isRecipient {
			writeError(w, r, errInsufficientScope)
			return
		}
	default:
		writeError(w, r, errInvalidParams)
		return
	}

	if state == db.RequestAccepted {
		if err := s.acceptRequest(r.Context(), req); err != nil {
			writeInternalError(s.log, w, r, "accept friend request", err)
			return
		}
	} else if err := s.store.SetFriendRequestState(r.Context(), req.ID, state, s.now()); err != nil {
		writeInternalError(s.log, w, r, "resolve friend request", err)
		return
	}

	s.log.Info("baas: friend request resolved",
		"request", req.ID, "from", req.FromUserID, "to", req.ToUserID,
		"state", state, "by", c.UserID)

	req.State = state
	req.UpdatedAt = s.now()
	writeJSON(w, http.StatusOK, s.friendRequestResponse(r.Context(), req, c.UserID))
}

// acceptRequest marks a request accepted and creates the friendship.
//
// The friendship is written first. If the state update then failed, the two are
// friends and a stale pending request remains, which the console shows as a
// duplicate — annoying. If the state were written first and the friendship
// failed, the request would look answered while nothing happened, which is
// unrecoverable without an operator. Getting the order right is what decides
// which of those a partial failure leaves behind.
func (s *Server) acceptRequest(ctx context.Context, req db.FriendRequest) error {
	now := s.now()
	if err := s.store.AddFriendship(ctx, req.FromUserID, req.ToUserID, now); err != nil {
		return err
	}
	return s.store.SetFriendRequestState(ctx, req.ID, db.RequestAccepted, now)
}

// parseRequestState reads the target state out of a PATCH body.
func parseRequestState(r *http.Request) (string, error) {
	raw, err := readBody(r, maxPatchBytes)
	if err != nil {
		return "", err
	}

	// A JSON patch is an array; a plain object is not. Trying the array first
	// distinguishes them without guessing from the content type, which the
	// console does not set consistently.
	if state, ok := stateFromPatchArray(raw); ok {
		return state, nil
	}

	var obj struct {
		State string `json:"state"`
		// 'accept' as a boolean is a plausible alternative shape.
		Accept *bool `json:"accept"`
	}
	if err := unmarshalJSON(raw, &obj); err != nil {
		return "", err
	}
	if obj.State != "" {
		return strings.ToUpper(obj.State), nil
	}
	if obj.Accept != nil {
		if *obj.Accept {
			return db.RequestAccepted, nil
		}
		return db.RequestDenied, nil
	}
	return "", errors.New("baas: no state in friend request patch")
}

func stateFromPatchArray(raw []byte) (string, bool) {
	var ops []patchOp
	if err := unmarshalJSON(raw, &ops); err != nil {
		return "", false
	}
	for _, op := range ops {
		if op.Path != "/state" {
			continue
		}
		if v, ok := op.stringValue(); ok {
			return strings.ToUpper(v), true
		}
	}
	return "", false
}

// ---- inbox and outbox --------------------------------------------------

func (s *Server) listInbox(w http.ResponseWriter, r *http.Request, c *caller) {
	s.listRequests(w, r, c, true)
}

func (s *Server) listOutbox(w http.ResponseWriter, r *http.Request, c *caller) {
	s.listRequests(w, r, c, false)
}

func (s *Server) listRequests(w http.ResponseWriter, r *http.Request, c *caller, incoming bool) {
	if !s.requireSelf(w, r, c) {
		return
	}

	requests, err := s.store.ListFriendRequests(r.Context(), c.UserID, incoming, true)
	if err != nil {
		writeInternalError(s.log, w, r, "list friend requests", err)
		return
	}

	out := make([]FriendRequestResponse, 0, len(requests))
	for _, req := range requests {
		// A request from or to somebody who has since been blocked should not
		// appear. Leaving it in the inbox means a blocked person can still put
		// a row on somebody's screen.
		other := req.FromUserID
		if !incoming {
			other = req.ToUserID
		}
		blocked, err := s.store.IsBlockedEitherWay(r.Context(), c.UserID, other)
		if err != nil {
			writeInternalError(s.log, w, r, "check blocks", err)
			return
		}
		if blocked {
			continue
		}
		out = append(out, s.friendRequestResponse(r.Context(), req, c.UserID))
	}
	writeJSON(w, http.StatusOK, newCollection(defaultFriendRequestCount, out))
}

// friendRequestResponse builds the wire object for a request, populating
// whichever party is not the caller.
func (s *Server) friendRequestResponse(ctx context.Context, req db.FriendRequest, callerID string) FriendRequestResponse {
	out := FriendRequestResponse{
		ID:        req.ID,
		State:     req.State,
		CreatedAt: req.CreatedAt.Unix(),
		UpdatedAt: req.UpdatedAt.Unix(),
	}

	fill := func(id string) *UserResponse {
		user, err := s.store.GetUser(ctx, id)
		if err != nil {
			s.log.Warn("baas: friend request names a missing user",
				"request", req.ID, "user", id, "error", err)
			return nil
		}
		pres, err := s.effectivePresence(ctx, id)
		if err != nil {
			s.log.Warn("baas: load presence for friend request", "user", id, "error", err)
			pres = db.Presence{UserID: id, State: db.StateOffline, Extras: db.EmptyExtras()}
		}
		// A pending request is not a friendship, so the sender only sees
		// stranger-visible fields — enough to decide whether to accept.
		u := userResponse(s.now(), user, s.friendCodeOrNil(ctx, id), pres, nil, visStranger, s.defaultAvatarURLOrEmpty())
		return &u
	}

	if req.FromUserID != callerID {
		out.From = fill(req.FromUserID)
	}
	if req.ToUserID != callerID {
		out.To = fill(req.ToUserID)
	}
	return out
}
