package baas

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/presence"
)

// defaultFriendCount is the page size the console asks for, and the cap applied
// when it does not ask.
const defaultFriendCount = 300

// maxFriendCount bounds what a single request can pull back, so a console (or
// something pretending to be one) cannot ask for the whole table at once.
const maxFriendCount = 1000

// defaultFriendRequestCount is the default page size for inbox and outbox.
const defaultFriendRequestCount = 50

// ---- GET /2.0.0/users/<id>/friends --------------------------------------

// listFriends returns the caller's friend list with each friend's presence.
//
// Each entry is a relationship object wrapping the friend's profile. The response is a
// CollectionEnvelope.
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
			Extras:     friendshipExtras(f.Extras),
			FriendNote: "",
			ETag:       "",
			CreatedAt:  f.BecameFriendAt.Unix(),
			UpdatedAt:  f.BecameFriendAt.Unix(),
		})
	}

	writeJSON(w, http.StatusOK, newCollection(count, out))
}

// presenceVisibleTo reports whether viewer may see owner's presence. The rule
// itself lives in internal/presence, shared with NPLN's Friends service.
func (s *Server) presenceVisibleTo(ctx context.Context, owner db.User, viewerID string) bool {
	return presence.Visible(ctx, s.store, owner, viewerID)
}

// ---- PATCH /2.0.0/users/<id>/friends/<id> -------------------------------

// patchFriend changes one direction of a friendship: the favourite flag, and
// anything under /extras/<scope>/<key>.
//
// Both are per-direction: A marking B as a favourite, or as seen, says nothing
// about what B sees. The extras paths are what the console itself sends -- it
// PATCHes /extras/self/isConfirmed onto a new friend, which must be accepted.
// Their meaning is the console's; this stores them and hands them back on the
// friend list.
func (s *Server) patchFriend(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}
	other := strings.ToLower(r.PathValue("other"))

	ops, err := parseJSONPatch(w, r)
	if err != nil || len(ops) == 0 {
		writeError(w, r, errInvalidParams)
		return
	}

	var favorite *bool
	var extras db.ExtrasByScope
	for _, op := range ops {
		if op.Path == "/isFavorite" || op.Path == "/favorite" {
			v, ok := op.boolValue()
			if !ok {
				writeError(w, r, errInvalidParams)
				return
			}
			favorite = &v
			continue
		}
		scope, key, ok := parseExtrasPath(op.Path)
		if !ok {
			s.log.Warn("baas: unsupported friend patch path",
				"user", c.UserID, "friend", other, "path", op.Path)
			writeError(w, r, errInvalidParams)
			return
		}
		if extras == nil {
			f, err := s.store.GetFriendship(r.Context(), c.UserID, other)
			switch {
			case errors.Is(err, db.ErrNotFound):
				writeError(w, r, errResourceNotFound)
				return
			case err != nil:
				writeInternalError(s.log, w, r, "load friendship", err)
				return
			}
			extras = f.Extras
			if extras == nil {
				extras = db.ExtrasByScope{}
			}
		}
		if op.Op == "remove" {
			delete(extras[scope], key)
			continue
		}
		if extras[scope] == nil {
			extras[scope] = map[string]any{}
		}
		extras[scope][key] = op.Value
	}

	if favorite != nil {
		switch err := s.store.SetFavorite(r.Context(), c.UserID, other, *favorite); {
		case errors.Is(err, db.ErrNotFound):
			writeError(w, r, errResourceNotFound)
			return
		case err != nil:
			writeInternalError(s.log, w, r, "set favourite", err)
			return
		}
	}
	if extras != nil {
		switch err := s.store.SetFriendshipExtras(r.Context(), c.UserID, other, extras); {
		case errors.Is(err, db.ErrNotFound):
			writeError(w, r, errResourceNotFound)
			return
		case err != nil:
			writeInternalError(s.log, w, r, "set friendship extras", err)
			return
		}
	}

	s.log.Info("baas: friendship updated", "user", c.UserID, "friend", other,
		"favorite", favorite != nil, "extras", extras != nil)
	w.WriteHeader(http.StatusNoContent)
}

// friendshipExtras is a friend-list entry's extras: the default scopes every
// entry has always carried, with whatever the console stored on this direction
// of the friendship on top.
func friendshipExtras(stored db.ExtrasByScope) db.ExtrasByScope {
	out := db.EmptyExtras()
	for scope, kv := range stored {
		if out[scope] == nil {
			out[scope] = map[string]any{}
		}
		for k, v := range kv {
			out[scope][k] = v
		}
	}
	return out
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
	s.notifyFriendDeleted(r.Context(), other)
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

	resp := RelationshipResponse{Type: RelationshipNone, SentFriendRequestIDs: []string{}}

	blocked, err := s.store.IsBlockedEitherWay(r.Context(), c.UserID, other)
	if err != nil {
		writeInternalError(s.log, w, r, "check blocks", err)
		return
	}
	// The client's isBlocking describes the caller's own action, which is not
	// the same question as the either-way suppression above. See
	// RelationshipResponse.IsBlocking.
	iBlock, err := s.store.IsBlocking(r.Context(), c.UserID, other)
	if err != nil {
		writeInternalError(s.log, w, r, "check block direction", err)
		return
	}
	resp.IsBlocking = iBlock

	switch {
	case blocked:
		resp.Type = RelationshipBlocked

	default:
		if f, err := s.store.GetFriendship(r.Context(), c.UserID, other); err == nil {
			resp.Type = RelationshipFriend
			resp.IsFriend = true
			resp.IsFavorite = f.Favorite
			resp.BecameFriendAt = f.BecameFriendAt.Unix()
			break
		}
		if req, err := s.store.FindPendingRequest(r.Context(), c.UserID, other); err == nil {
			resp.Type = RelationshipSent
			// The client only asks whether this array has a first element, so
			// one entry says "you have a request outstanding" without
			// inventing a list this server does not keep.
			resp.SentFriendRequestIDs = []string{req.ID}
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
		thumb1, thumb2 := s.defaultAvatarURLOrEmpty()
		u := userResponse(s.now(), user, s.friendCodeOrNil(r.Context(), other), pres, nil, vis, thumb1, thumb2)
		resp.User = &u
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---- POST /2.0.0/friend_requests ---------------------------------------

// createFriendRequestBody is the request body.
//
// UNCONFIRMED: undocumented. The friends applet can add a friend two ways -- by
// typing a friend code, and by picking somebody the console already knows about
// -- so both a code and a user id are accepted. Form encoding and JSON are both
// accepted for the same reason: which one the sysmodule uses is not documented,
// and refusing the wrong guess would be a dead end during bring-up.
//
// ReceiverID is the shape a real console sends when adding by friend code: after
// the console resolves a friend code to a profile itself (the preceding GET
// /1.0.0/users and GET /2.0.0/users/<id>/relationships/<id> calls), the create
// POST names the target directly, not by code, with this body:
//
//	{"channels":["FRIEND_CODE"],"senderId":"<caller>","receiverId":"<target>",
//	 "extras":{"sender":{"route:sender":"me"}}}
//
// i.e. the same senderId/receiverId/channels/extras shape FriendRequestResponse
// itself carries, not a lookup payload. FriendCode/UserID/NSAID were this
// server's own guess at what a create request would look like; kept alongside
// ReceiverID rather than removed, since nothing has shown they are wrong, only
// that they are not what this particular flow (friend-code entry) uses. senderId
// is read but not required to equal the caller: the caller's own identity is what
// authorizes the request either way (requireSelf-style fail-closed identity), so
// a mismatched senderId is not itself trusted for anything, just present for
// symmetry with the response shape.
type createFriendRequestBody struct {
	// FriendCode is the twelve-digit code, with or without dashes and with or
	// without the "SW-" prefix.
	FriendCode string `json:"friendCode"`
	// UserID is the target's BAAS user id.
	UserID string `json:"userId"`
	// NSAID is another name the console uses for the same thing.
	NSAID string `json:"nsaId"`
	// ReceiverID is the target's BAAS user id, in the shape a real console
	// sends when it already resolved the target itself. See this type's own
	// doc comment above.
	ReceiverID string `json:"receiverId"`
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

	req, err := s.sendFriendRequest(r.Context(), c.UserID, target)
	switch {
	case errors.Is(err, errSelfFriendRequest):
		writeError(w, r, errInvalidParams)
		return
	case errors.Is(err, errFriendRequestRefused):
		writeError(w, r, errForbidden)
		return
	case errors.Is(err, errAlreadyFriends):
		writeError(w, r, errResourceAlreadyExists)
		return
	case err != nil:
		writeInternalError(s.log, w, r, "send friend request", err)
		return
	}
	if req.State == db.RequestAccepted {
		writeJSON(w, http.StatusCreated, FriendRequestResponse{
			ID:         wireRequestID(req.ID),
			SenderID:   req.FromUserID,
			ReceiverID: req.ToUserID,
			State:      requestStateToWire(db.RequestAccepted),
			CreatedAt:  req.CreatedAt.Unix(),
			UpdatedAt:  s.now().Unix(),
		})
		return
	}
	writeJSON(w, http.StatusCreated, s.friendRequestResponse(r.Context(), req, c.UserID))
}

var (
	errSelfFriendRequest    = errors.New("friend request to oneself")
	errFriendRequestRefused = errors.New("target does not accept friend requests from the sender")
	errAlreadyFriends       = errors.New("already friends")
)

// sendFriendRequest is every rule about adding a friend, shared by the
// console's endpoint and the Discord bot's. A request crossing one the target
// already sent is accepted instead: the returned request is then that
// incoming one, in state RequestAccepted.
func (s *Server) sendFriendRequest(ctx context.Context, fromID string, target db.User) (db.FriendRequest, error) {
	if target.ID == fromID {
		// Refusing this is not pedantry. When identity resolution upstream is
		// broken, "add a friend" becomes a self-request surprisingly often, and
		// a friend graph full of self-edges is very hard to read back.
		s.log.Warn("baas: refused self friend request", "user", fromID)
		return db.FriendRequest{}, errSelfFriendRequest
	}

	blocked, err := s.store.IsBlockedEitherWay(ctx, fromID, target.ID)
	if err != nil {
		return db.FriendRequest{}, err
	}
	// A block answers the same as "does not accept requests", so a blocked
	// sender cannot tell a block apart from a preference.
	if blocked || !target.Permissions.FriendRequestReception {
		return db.FriendRequest{}, errFriendRequestRefused
	}

	if _, err := s.store.GetFriendship(ctx, fromID, target.ID); err == nil {
		return db.FriendRequest{}, errAlreadyFriends
	}

	// If the target already asked us, accept instead of queueing a second
	// request in the other direction. Two people who add each other at the same
	// moment should end up friends, not with an unanswered request each.
	if incoming, err := s.store.FindPendingRequest(ctx, target.ID, fromID); err == nil {
		if err := s.acceptRequest(ctx, incoming); err != nil {
			return db.FriendRequest{}, err
		}
		s.log.Info("baas: friend requests crossed, became friends",
			"user", fromID, "friend", target.ID)
		incoming.State = db.RequestAccepted
		return incoming, nil
	}

	now := s.now()
	req, err := s.store.CreateFriendRequest(ctx, db.FriendRequest{
		ID:         uuid.NewString(),
		FromUserID: fromID,
		ToUserID:   target.ID,
		State:      db.RequestPending,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	if err != nil {
		return db.FriendRequest{}, err
	}
	s.log.Info("baas: friend request sent", "from", fromID, "to", target.ID, "request", req.ID)
	// The friends module reads the request under $.value, in the inbox's own item shape.
	s.notify(ctx, target.ID, "friend_request_received", map[string]any{
		"type": "friend_request_received", "value": s.friendRequestResponse(ctx, req, target.ID),
	})
	return req, nil
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
	body.ReceiverID = f.Get("receiverId")
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
	if id == "" {
		id = strings.ToLower(body.ReceiverID)
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
// UNCONFIRMED: the body is undocumented. Both a JSON patch on /state
// and a plain {"state":"..."} object are accepted, because either is a plausible
// shape for a PATCH and guessing wrong would stop friend requests working with
// nothing in the log to explain it.
func (s *Server) resolveFriendRequest(w http.ResponseWriter, r *http.Request, c *caller) {
	state, err := parseRequestState(r)
	if errors.Is(err, errMarksRead) {
		// Opening a request's sender profile. This server keeps no read
		// state, so it changes nothing; refusing it was 2121-2001 on the
		// profile. Still only the recipient's own request answers.
		req, err := s.findFriendRequest(r.Context(), c.UserID, r.PathValue("rid"))
		if err != nil || req.ToUserID != c.UserID {
			writeError(w, r, errResourceNotFound)
			return
		}
		writeJSON(w, http.StatusOK, s.friendRequestResponse(r.Context(), req, c.UserID))
		return
	}
	if err != nil {
		// Checked after the request itself only matters for the order of
		// errors; an unreadable body is a bad request either way.
		state = ""
	}
	req, err := s.settleFriendRequest(r.Context(), c.UserID, r.PathValue("rid"), state)
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, r, errResourceNotFound)
		return
	case errors.Is(err, errNotYourRequest):
		writeError(w, r, errInsufficientScope)
		return
	case errors.Is(err, errRequestNotPending):
		writeError(w, r, errResourceAlreadyExists)
		return
	case errors.Is(err, errBadRequestState):
		writeError(w, r, errInvalidParams)
		return
	case err != nil:
		writeInternalError(s.log, w, r, "resolve friend request", err)
		return
	}
	writeJSON(w, http.StatusOK, s.friendRequestResponse(r.Context(), req, c.UserID))
}

// wireRequestID is a request's id as the console handles it: 16 hex digits.
// The friends sysmodule reads $.items[*].id as a u64 and builds its
// accept/reject URL with "friend_requests/%016llx"; a UUID does not parse, and
// the received-requests screen failed with 2121-0104 on the first row. The
// stored id stays a UUID; its first 64 bits name it on the wire.
func wireRequestID(id string) string {
	hex := strings.ReplaceAll(id, "-", "")
	if len(hex) < 16 {
		return hex
	}
	return hex[:16]
}

// findFriendRequest resolves an id from the console (wire form) or anything
// else that still holds a full UUID, among the caller's own pending requests.
func (s *Server) findFriendRequest(ctx context.Context, callerID, rid string) (db.FriendRequest, error) {
	if len(rid) != 16 {
		return s.store.GetFriendRequest(ctx, rid)
	}
	rid = strings.ToLower(rid)
	for _, incoming := range []bool{true, false} {
		reqs, err := s.store.ListFriendRequests(ctx, callerID, incoming, true)
		if err != nil {
			return db.FriendRequest{}, err
		}
		for _, req := range reqs {
			if wireRequestID(req.ID) == rid {
				return req, nil
			}
		}
	}
	return db.FriendRequest{}, db.ErrNotFound
}

var (
	errNotYourRequest    = errors.New("caller may not do that to this friend request")
	errRequestNotPending = errors.New("friend request is no longer pending")
	errBadRequestState   = errors.New("not a state a friend request can be moved to")
)

// settleFriendRequest accepts, denies or cancels a pending request. Only the
// two parties may touch it, and each only their own half: the recipient
// accepts or denies, the sender cancels.
func (s *Server) settleFriendRequest(ctx context.Context, callerID, rid, state string) (db.FriendRequest, error) {
	req, err := s.findFriendRequest(ctx, callerID, rid)
	if err != nil {
		return db.FriendRequest{}, err
	}
	if callerID != req.FromUserID && callerID != req.ToUserID {
		s.log.Warn("baas: refused friend request action by a third party",
			"caller", callerID, "request", rid)
		return db.FriendRequest{}, errNotYourRequest
	}
	if req.State != db.RequestPending {
		return db.FriendRequest{}, errRequestNotPending
	}

	isRecipient := callerID == req.ToUserID
	switch state {
	case db.RequestAccepted, db.RequestDenied:
		if !isRecipient {
			return db.FriendRequest{}, errNotYourRequest
		}
	case db.RequestCancelled:
		if isRecipient {
			return db.FriendRequest{}, errNotYourRequest
		}
	default:
		return db.FriendRequest{}, errBadRequestState
	}

	if state == db.RequestAccepted {
		if err := s.acceptRequest(ctx, req); err != nil {
			return db.FriendRequest{}, err
		}
	} else if err := s.store.SetFriendRequestState(ctx, req.ID, state, s.now()); err != nil {
		return db.FriendRequest{}, err
	}

	s.log.Info("baas: friend request resolved",
		"request", req.ID, "from", req.FromUserID, "to", req.ToUserID,
		"state", state, "by", callerID)
	req.State = state
	req.UpdatedAt = s.now()
	return req, nil
}

// acceptRequest marks a request accepted and creates the friendship, in
// one transaction. Writing them separately raced a concurrent cancel or
// block: the friendship was committed, then the state update found the
// request no longer pending and the accept failed -- leaving two people
// friends through a request that had been withdrawn.
func (s *Server) acceptRequest(ctx context.Context, req db.FriendRequest) error {
	if err := s.store.AcceptFriendRequest(ctx, req.ID, s.now()); err != nil {
		return err
	}
	// The sender's console refreshes its friend list on this (the friends module reads
	// nothing but the type).
	s.notify(ctx, req.FromUserID, "friend_request_authorized", map[string]any{"type": "friend_request_authorized"})
	return nil
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
		return requestStateFromWire(state), nil
	}
	if onlyMarksRead(raw) {
		return "", errMarksRead
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
		return requestStateFromWire(strings.ToUpper(obj.State)), nil
	}
	if obj.Accept != nil {
		if *obj.Accept {
			return db.RequestAccepted, nil
		}
		return db.RequestDenied, nil
	}
	return "", errors.New("baas: no state in friend request patch")
}

// requestStateToWire is requestStateFromWire's inverse: this server's
// internal constant, translated to what the console's own parser actually
// recognizes. Every place a FriendRequestResponse.State is set must go
// through this -- sending "ACCEPTED" instead of "AUTHORIZED" would be as
// unrecognizable to the console as sending "AUTHORIZED" instead of "ACCEPTED"
// would be to this server's own PATCH parser. See requestStateFromWire.
func requestStateToWire(internal string) string {
	switch internal {
	case db.RequestAccepted:
		return "AUTHORIZED"
	case db.RequestDenied:
		return "REJECTED"
	case db.RequestCancelled:
		return "CANCELED"
	default:
		return internal
	}
}

// requestStateFromWire translates the console's own spelling onto this
// server's internal state constants.
//
// The enum the PATCH body's "state" value is drawn from is PENDING, AUTHORIZED,
// REJECTED, CANCELED, EXPIRED: AUTHORIZED, not ACCEPTED; REJECTED, not DENIED;
// CANCELED (one L), not CANCELLED. Without this translation, a real console
// accepting or declining a friend request would send a value this server's
// switch in resolveFriendRequest does not recognize, fail with
// errInvalidParams, and accepting a friend request would not work at all. Both
// spellings are still accepted below: the real one so a genuine console works,
// the old one so nothing already relying on it (tests, tooling) breaks.
//
// EXPIRED is deliberately not mapped to anything: nothing in this server ever
// assigns it, a client has no reason to PATCH a request into it, and unlike the
// other four it reads as a state the SERVER would set after a timeout this
// project does not implement, not one a client requests.
func requestStateFromWire(raw string) string {
	switch raw {
	case "AUTHORIZED":
		return db.RequestAccepted
	case "REJECTED":
		return db.RequestDenied
	case "CANCELED":
		return db.RequestCancelled
	default:
		return raw
	}
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

// errMarksRead is a patch that only sets /extras/receiver/read: the console
// sends it when the recipient opens the sender's profile from the inbox.
var errMarksRead = errors.New("friend request patch only marks it read")

func onlyMarksRead(raw []byte) bool {
	var ops []patchOp
	if err := unmarshalJSON(raw, &ops); err != nil || len(ops) == 0 {
		return false
	}
	for _, op := range ops {
		if op.Path != "/extras/receiver/read" {
			return false
		}
	}
	return true
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

// friendRequestResponse builds the wire object for a request.
//
// Sender and Receiver are filled for BOTH parties, including the caller's own
// side: the client's parser (`$.items[*].receiver.nickname`,
// `$.items[*].sender.nickname`, ...) reads both sides unconditionally, with
// nothing suggesting either is ever skipped. 2124-0104 and 2121-0104
// (switchbrew: ResultUserUnqualified, the client's own generic "response didn't
// have what I needed" code, raised inside the applet, not from a bad HTTP
// status) on the friend-requests screen are consistent with a parser reading a
// field this server leaves unset.
func (s *Server) friendRequestResponse(ctx context.Context, req db.FriendRequest, callerID string) FriendRequestResponse {
	out := FriendRequestResponse{
		ID:         wireRequestID(req.ID),
		SenderID:   req.FromUserID,
		ReceiverID: req.ToUserID,
		State:      requestStateToWire(req.State),
		CreatedAt:  req.CreatedAt.Unix(),
		// channels[0] is required: the inbox/outbox parser (0x1cf640) aborts
		// an item that lacks any of id, channels[0], state, senderId,
		// sender.nickname or sender.thumbnailUrl, and RapidJSON's
		// kParseErrorTermination becomes 2121-0104 (table 0x222448). An empty
		// array failed the received-requests screen. The console sends
		// FRIEND_CODE when it creates a request and this server stores no
		// channel, so it is echoed back for every request.
		Channels: []any{"FRIEND_CODE"},
		// Extras' three scopes (receiver/sender/senderAndReceiver) are the ones the
		// parser reads (extras.receiver.read, extras.sender.route:*,
		// extras.senderAndReceiver.route:*) -- this server keeps no read state for a
		// request and no route:* content for either scope, so sender and
		// senderAndReceiver stay real, present, empty maps. receiver.read is set false
		// (unread) as the more useful default for a pending request the receiver has not
		// acted on yet.
		//
		// UNCONFIRMED: whether the client treats an absent key as unread or read, so this
		// value is this server's own choice. Change here once a real client is seen
		// marking a request read.
		Extras: map[string]map[string]any{
			"receiver":          {"read": false},
			"sender":            {},
			"senderAndReceiver": {},
		},
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
		// A pending request is not a friendship, so each side only sees
		// stranger-visible fields of the other -- enough to decide whether
		// to accept. The caller's own side is filled the same way: see
		// this function's own doc comment for why that changed.
		thumb1, thumb2 := s.defaultAvatarURLOrEmpty()
		u := userResponse(s.now(), user, s.friendCodeOrNil(ctx, id), pres, nil, visStranger, thumb1, thumb2)
		return &u
	}

	out.Sender = fill(req.FromUserID)
	out.Receiver = fill(req.ToUserID)
	return out
}

// notifyFriendDeleted tells the other side's consoles to drop the friend (the
// friends module reads nothing but the type).
func (s *Server) notifyFriendDeleted(ctx context.Context, userID string) {
	s.notify(ctx, userID, "friend_deleted", map[string]any{"type": "friend_deleted"})
}
