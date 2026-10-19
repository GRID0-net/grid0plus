package baas

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/presencebus"
)

// The emulator API is what an emulator's friend service reads its friends and
// writes its presence through. A console's nn::friends sysmodule speaks the BAAS
// 2.0.0 friends protocol and keeps its own cache; an emulator has no such sysmodule,
// so its HLE friend service asks here instead, with the BAAS access token its
// username/password login (POST /login) already holds.
//
// The ids are NSA ids, the same ids NPLN names friends by. Splatoon 3 takes its friend
// list from NPLN, then asks the local friend service about each friend by that id; an
// emulator that answered with any other id showed an empty in-game friend list.

type emulatorFriend struct {
	ID         string `json:"id"`
	Nickname   string `json:"nickname"`
	Login      string `json:"login,omitempty"`
	FriendCode string `json:"friendCode,omitempty"`
	// State is the effective presence: ONLINE, PLAYING or OFFLINE, empty when
	// the friend hides it from the caller.
	State string `json:"state"`
	AppID string `json:"appId,omitempty"`
	// AppField is the game's own presence blob, as the friend reported it.
	// An emulator reports its nn::friends app key-value storage base64-encoded.
	AppField   string `json:"appField,omitempty"`
	ImageURL   string `json:"imageUrl,omitempty"`
	IsFavorite bool   `json:"isFavorite,omitempty"`
}

func (s *Server) registerEmulatorRoutes(mux *http.ServeMux) {
	mux.Handle("GET /emulator/v1/me", s.authenticated(s.emulatorMe))
	mux.Handle("GET /emulator/v1/friends", s.authenticated(s.emulatorFriends))
	mux.Handle("POST /emulator/v1/presence", s.authenticated(s.emulatorPresence))
	mux.Handle("GET /emulator/v1/friend-requests", s.authenticated(s.emulatorFriendRequests))
	mux.Handle("POST /emulator/v1/friend-requests", s.authenticated(s.emulatorSendFriendRequest))
	mux.Handle("POST /emulator/v1/friend-requests/{id}/{action}", s.authenticated(s.emulatorSettleFriendRequest))
	mux.Handle("DELETE /emulator/v1/friends/{id}", s.authenticated(s.emulatorRemoveFriend))
	mux.Handle("GET /emulator/v1/bcat/{title}", s.authenticated(s.emulatorBCATList))
	mux.Handle("GET /emulator/v1/bcat/{title}/{dir}/{file}", s.authenticated(s.emulatorBCATFile))
}

func (s *Server) emulatorUser(w http.ResponseWriter, r *http.Request, id string, p *db.Presence, visible bool) emulatorFriend {
	u, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		return emulatorFriend{ID: id}
	}
	out := emulatorFriend{ID: u.ID, Nickname: u.Nickname, Login: u.LoginUsername, ImageURL: u.ThumbnailURL}
	if out.Nickname == "" {
		out.Nickname = u.LoginUsername
	}
	if out.ImageURL == "" {
		out.ImageURL, _ = s.defaultAvatarURLOrEmpty()
	}
	if fc, err := s.store.GetFriendCode(r.Context(), u.ID); err == nil {
		out.FriendCode = fc.Code
	}
	if p != nil && visible {
		out.State, out.AppID, out.AppField = p.State, p.AppID, p.AppField
	}
	return out
}

func (s *Server) emulatorMe(w http.ResponseWriter, r *http.Request, c *caller) {
	p, _ := s.effectivePresence(r.Context(), c.UserID)
	writeJSON(w, http.StatusOK, s.emulatorUser(w, r, c.UserID, &p, true))
}

func (s *Server) emulatorFriends(w http.ResponseWriter, r *http.Request, c *caller) {
	friendships, err := s.store.ListFriends(r.Context(), c.UserID, 300)
	if err != nil {
		writeInternalError(s.log, w, r, "list friends", err)
		return
	}
	ids := make([]string, 0, len(friendships))
	for _, f := range friendships {
		ids = append(ids, f.FriendID)
	}
	presences, err := s.effectivePresences(r.Context(), ids)
	if err != nil {
		writeInternalError(s.log, w, r, "load presences", err)
		return
	}

	out := make([]emulatorFriend, 0, len(friendships))
	for _, f := range friendships {
		owner, err := s.store.GetUser(r.Context(), f.FriendID)
		if err != nil || owner.Deleted {
			continue
		}
		p := presences[f.FriendID]
		entry := s.emulatorUser(w, r, f.FriendID, &p, s.presenceVisibleTo(r.Context(), owner, c.UserID))
		entry.IsFavorite = f.Favorite
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Nickname) < strings.ToLower(out[j].Nickname) })
	writeJSON(w, http.StatusOK, map[string]any{"friends": out})
}

type emulatorPresenceBody struct {
	// State is ONLINE, PLAYING or OFFLINE.
	State    string `json:"state"`
	AppID    string `json:"appId"`
	AppField string `json:"appField"`
}

// emulatorPresence records what the emulator is doing: the nn::friends
// status the game set, the title running, and the game's presence blob, which
// is what lets a friend join.
func (s *Server) emulatorPresence(w http.ResponseWriter, r *http.Request, c *caller) {
	var body emulatorPresenceBody
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, r, errInvalidParams)
		return
	}
	switch body.State {
	case db.StateOnline, db.StatePlaying, db.StateOffline:
	default:
		writeError(w, r, errInvalidParams)
		return
	}
	if len(body.AppField) > 1024 || len(body.AppID) > 32 {
		writeError(w, r, errInvalidParams)
		return
	}
	// The forks pass the game's own status through, and in a game that is
	// mostly 1, ONLINE, with the title id set, the same as a console's npns
	// reports; BAAS clears the app of anything not PLAYING, so friends read
	// "Online". The forks send no id outside a game, so one here is current.
	if body.State == db.StateOnline && strings.Trim(body.AppID, "0") != "" {
		body.State = db.StatePlaying
	}
	now := s.now()
	prev, err := s.store.GetPresence(r.Context(), c.UserID)
	if err != nil {
		writeInternalError(s.log, w, r, "load presence", err)
		return
	}
	patch := db.PresencePatch{State: &body.State, AppID: &body.AppID, AppField: &body.AppField, ReportedAt: &now}
	if body.State == db.StateOffline {
		patch.LogoutAt = &now
	}
	updated, err := s.store.UpdatePresence(r.Context(), c.UserID, patch)
	if err != nil {
		writeInternalError(s.log, w, r, "update presence", err)
		return
	}
	s.publishPresence(r.Context(), presencebus.Event{
		UserID: c.UserID, State: updated.State, AppID: updated.AppID,
		AppField: updated.AppField, At: now, Source: "emulator",
	})
	s.recordEmulatorPlay(r.Context(), c.UserID, prev, body.State, body.AppID, now)
	w.WriteHeader(http.StatusNoContent)
}

type emulatorRequest struct {
	ID    string         `json:"id"`
	Other emulatorFriend `json:"other"`
}

func (s *Server) emulatorFriendRequests(w http.ResponseWriter, r *http.Request, c *caller) {
	list := func(incoming bool) ([]emulatorRequest, error) {
		reqs, err := s.store.ListFriendRequests(r.Context(), c.UserID, incoming, true)
		if err != nil {
			return nil, err
		}
		out := make([]emulatorRequest, 0, len(reqs))
		for _, fr := range reqs {
			other := fr.ToUserID
			if incoming {
				other = fr.FromUserID
			}
			out = append(out, emulatorRequest{ID: fr.ID, Other: s.emulatorUser(w, r, other, nil, false)})
		}
		return out, nil
	}
	incoming, err := list(true)
	if err != nil {
		writeInternalError(s.log, w, r, "list requests", err)
		return
	}
	outgoing, err := list(false)
	if err != nil {
		writeInternalError(s.log, w, r, "list requests", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"incoming": incoming, "outgoing": outgoing})
}

// emulatorProblem is the error shape the emulator shows to the player.
func emulatorProblem(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

func (s *Server) emulatorSendFriendRequest(w http.ResponseWriter, r *http.Request, c *caller) {
	var body struct {
		FriendCode string `json:"friendCode"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, r, errInvalidParams)
		return
	}
	code := normaliseFriendCode(body.FriendCode)
	if code == "" {
		emulatorProblem(w, http.StatusBadRequest, "bad_friend_code", "A friend code is 12 digits.")
		return
	}
	found, err := s.store.FindUsers(r.Context(), db.UserQuery{FriendCode: code, Limit: 1})
	if err != nil {
		writeInternalError(s.log, w, r, "find by friend code", err)
		return
	}
	if len(found) == 0 || found[0].Deleted {
		emulatorProblem(w, http.StatusNotFound, "no_such_user", "Nobody has that friend code.")
		return
	}
	req, err := s.sendFriendRequest(r.Context(), c.UserID, found[0])
	switch {
	case errors.Is(err, errSelfFriendRequest):
		emulatorProblem(w, http.StatusBadRequest, "self", "That is your own friend code.")
		return
	case errors.Is(err, errFriendRequestRefused):
		emulatorProblem(w, http.StatusForbidden, "refused", "That player is not accepting friend requests.")
		return
	case errors.Is(err, errAlreadyFriends):
		emulatorProblem(w, http.StatusConflict, "already_friends", "You are already friends.")
		return
	case err != nil:
		writeInternalError(s.log, w, r, "send friend request", err)
		return
	}
	result := "sent"
	if req.State == db.RequestAccepted {
		result = "became_friends"
	}
	writeJSON(w, http.StatusCreated, map[string]string{"result": result, "id": req.ID})
}

func (s *Server) emulatorSettleFriendRequest(w http.ResponseWriter, r *http.Request, c *caller) {
	state, ok := map[string]string{
		"accept": db.RequestAccepted, "deny": db.RequestDenied, "cancel": db.RequestCancelled,
	}[r.PathValue("action")]
	if !ok {
		writeError(w, r, errResourceNotFound)
		return
	}
	_, err := s.settleFriendRequest(r.Context(), c.UserID, r.PathValue("id"), state)
	switch {
	case errors.Is(err, db.ErrNotFound), errors.Is(err, errNotYourRequest):
		emulatorProblem(w, http.StatusNotFound, "no_such_request", "That request no longer exists.")
		return
	case errors.Is(err, errRequestNotPending):
		emulatorProblem(w, http.StatusConflict, "not_pending", "That request was already answered.")
		return
	case err != nil:
		writeInternalError(s.log, w, r, "settle friend request", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) emulatorRemoveFriend(w http.ResponseWriter, r *http.Request, c *caller) {
	err := s.store.RemoveFriendship(r.Context(), c.UserID, strings.ToLower(r.PathValue("id")))
	if errors.Is(err, db.ErrNotFound) {
		emulatorProblem(w, http.StatusNotFound, "not_friends", "You are not friends.")
		return
	}
	if err != nil {
		writeInternalError(s.log, w, r, "remove friendship", err)
		return
	}
	s.notifyFriendDeleted(r.Context(), strings.ToLower(r.PathValue("id")))
	w.WriteHeader(http.StatusNoContent)
}
