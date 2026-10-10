package baas

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// A Switch friend code is twelve decimal digits, shown to players as
// SW-XXXX-XXXX-XXXX. The BAAS user object stores just the digits, grouped with
// dashes: "7779-8151-4794".

// friendCodeGroups is how many four-digit groups a code has.
const friendCodeGroups = 3

// friendCodeAttempts is how many times a collision is retried before giving up.
//
// The space is 10^12. With a group of friends on one server, a collision is
// vanishingly unlikely, so five attempts is not a rate limit — it is a guard
// against looping forever if the uniqueness constraint is failing for some other
// reason.
const friendCodeAttempts = 5

// defaultFriendCodeCooldown is the regeneration cooldown used for a code
// assigned at account-creation time, when there is no *Server (and so no
// configured s.cfg.FriendCodeCooldown) to read from -- ProvisionDeviceAccount
// is called from switchnetctl, not from a running server. Matches
// config.example.yaml's own default for friend_code_cooldown.
const defaultFriendCodeCooldown = 24 * time.Hour

// ---- POST /1.0.0/users/<id>/generate_code -------------------------------

// generateFriendCode issues a new friend code.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#post-100usersidgenerate_code
//
// After generating one, a user cannot generate another for 24 hours. The cooldown
// is not decoration: without it, anybody who was given a code can be locked out
// of using it by the owner regenerating on a loop, and with it a code stays
// usable long enough to be typed in.
func (s *Server) generateFriendCode(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}

	form, err := parseForm(r)
	if err != nil {
		writeError(w, r, errInvalidParams)
		return
	}
	// The only documented type is NX. An empty value is tolerated because the
	// parameter is trivially defaulted and refusing it would block the feature
	// on a firmware that stopped sending it.
	if t := form.Get("type"); t != "" && t != "NX" {
		writeError(w, r, errInvalidParams)
		return
	}

	now := s.now()

	existing, err := s.store.GetFriendCode(r.Context(), c.UserID)
	switch {
	case err == nil:
		if !existing.Regenerable(now) {
			s.log.Info("baas: friend code regeneration refused, still on cooldown",
				"user", c.UserID, "regenerable_at", existing.RegenerableAt)
			writeError(w, r, errFriendCodeUnregenerable)
			return
		}
	case errors.Is(err, db.ErrNotFound):
		// First code for this user.
	default:
		writeInternalError(s.log, w, r, "load friend code", err)
		return
	}

	fc, err := assignFriendCode(r.Context(), s.store, now, c.UserID, s.cfg.FriendCodeCooldown)
	if err != nil {
		writeInternalError(s.log, w, r, "allocate friend code", err)
		return
	}

	s.log.Info("baas: friend code generated", "user", c.UserID, "code", fc.Code)

	user, err := s.store.GetUser(r.Context(), c.UserID)
	if err != nil {
		writeInternalError(s.log, w, r, "load user", err)
		return
	}
	s.writeSelf(w, r, user)
}

// assignFriendCode generates and stores a fresh friend code for a user that
// does not have one yet, retrying on a random collision.
//
// Every real account has a friend code from the moment it is created --
// System Settings shows one immediately, with no Nintendo Account link
// required -- but until 2026-09-21 this server only ever assigned one in
// response to an explicit POST .../generate_code, and nothing calls that
// automatically. A live mitmdump capture that night, taken against a
// federation attempt that ended in the console's "couldn't be linked"
// dialog, showed why that is a real bug and not just a cosmetic gap: the
// console's own /1.0.0/login response was missing user.links.friendCode
// entirely (diffed against a known-working reference capture), and the
// console never called generate_code itself, or push_channels, or
// GET users/{id} afterwards -- it silently abandoned the link instead of
// asking for a code. So the console expects a friend code to already exist
// by the time it reads the account back, not to be told to go create one.
// This is called from every path that creates a brand-new user (registerUser,
// ProvisionDeviceAccount, the website's self-registration), not just here.
func assignFriendCode(ctx context.Context, store db.Store, now time.Time, userID string, cooldown time.Duration) (db.FriendCode, error) {
	for range friendCodeAttempts {
		candidate, err := newFriendCode()
		if err != nil {
			return db.FriendCode{}, fmt.Errorf("generate friend code: %w", err)
		}
		fc := db.FriendCode{
			UserID:        userID,
			Code:          candidate,
			CreatedAt:     now,
			UpdatedAt:     now,
			RegenerableAt: now.Add(cooldown),
		}
		err = store.SetFriendCode(ctx, fc)
		switch {
		case err == nil:
			return fc, nil
		case errors.Is(err, db.ErrConflict):
			continue
		default:
			return db.FriendCode{}, err
		}
	}
	return db.FriendCode{}, fmt.Errorf("no unique friend code after %d attempts", friendCodeAttempts)
}

// newFriendCode returns a random twelve-digit code in dashed form.
func newFriendCode() (string, error) {
	groups := make([]string, friendCodeGroups)
	for i := range groups {
		n, err := rand.Int(rand.Reader, big.NewInt(10000))
		if err != nil {
			return "", fmt.Errorf("generate friend code: %w", err)
		}
		groups[i] = fmt.Sprintf("%04d", n.Int64())
	}
	return strings.Join(groups, "-"), nil
}

// normaliseFriendCode turns anything a player might type into the stored form.
//
// Players copy codes with the "SW-" prefix, without dashes, with spaces, or
// pasted out of a chat message. Accepting all of those is the difference between
// "add friend" working and a player being told their friend's code is invalid.
// Returns "" if the input is not twelve digits.
func normaliseFriendCode(input string) string {
	s := strings.TrimSpace(input)
	s = strings.TrimPrefix(strings.ToUpper(s), "SW-")

	var digits strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	if len(d) != friendCodeGroups*4 {
		return ""
	}
	return d[0:4] + "-" + d[4:8] + "-" + d[8:12]
}

// ---- blocks -------------------------------------------------------------

// listBlocks returns who the caller has blocked.
func (s *Server) listBlocks(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}

	blocks, err := s.store.ListBlocks(r.Context(), c.UserID)
	if err != nil {
		writeInternalError(s.log, w, r, "list blocks", err)
		return
	}

	out := make([]BlockResponse, 0, len(blocks))
	for _, b := range blocks {
		entry := BlockResponse{
			TargetUserID: b.BlockedUserID,
			CreatedAt:    b.CreatedAt.Unix(),
		}
		if user, err := s.store.GetUser(r.Context(), b.BlockedUserID); err == nil {
			// A blocked user's presence is not shown, so it is not looked up.
			thumb1, thumb2 := s.defaultAvatarURLOrEmpty()
			u := userResponse(s.now(), user, nil,
				db.Presence{UserID: user.ID, State: db.StateOffline, Extras: db.EmptyExtras()},
				nil, visStranger, thumb1, thumb2)
			entry.TargetUser = &u
		}
		out = append(out, entry)
	}

	// An envelope, not a bare array: the console's parser reads $.items[*] and
	// never matches anything at the top level. See BlockResponse's comment.
	requested := 100
	if c := r.URL.Query().Get("count"); c != "" {
		if n, err := strconv.Atoi(c); err == nil && n > 0 {
			requested = n
		}
	}
	writeJSON(w, http.StatusOK, newCollection(requested, out))
}

// blockRequestBody is the POST body.
//
// TODO(capture-needed): undocumented. Both form and JSON encodings are accepted,
// keyed on either userId or nsaId, for the same reason as the friend request
// body: which one the friends sysmodule sends is not documented.
type blockRequestBody struct {
	UserID string `json:"userId"`
	NSAID  string `json:"nsaId"`
}

// addBlock blocks a user.
//
// Blocking also removes an existing friendship and cancels any pending request
// in either direction. A block that leaves the friendship in place is not a
// block: the two would still see each other in their friend lists.
func (s *Server) addBlock(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}

	var body blockRequestBody
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	if strings.TrimSpace(ct) == "application/json" {
		if err := decodeJSONBody(r, &body); err != nil {
			writeError(w, r, errInvalidParams)
			return
		}
	} else {
		f, err := parseForm(r)
		if err != nil {
			writeError(w, r, errInvalidParams)
			return
		}
		body.UserID = f.Get("userId")
		body.NSAID = f.Get("nsaId")
	}

	target := strings.ToLower(body.UserID)
	if target == "" {
		target = strings.ToLower(body.NSAID)
	}
	if len(target) != 16 || target == c.UserID {
		writeError(w, r, errInvalidParams)
		return
	}

	now := s.now()
	if err := s.store.AddBlock(r.Context(), c.UserID, target, now); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, r, errResourceNotFound)
			return
		}
		writeInternalError(s.log, w, r, "add block", err)
		return
	}

	// Tear the relationship down. A failure here is logged rather than returned:
	// the block itself is recorded, and every read path already suppresses a
	// blocked pair, so the visible behaviour is correct either way.
	if err := s.store.RemoveFriendship(r.Context(), c.UserID, target); err != nil &&
		!errors.Is(err, db.ErrNotFound) {
		s.log.Warn("baas: remove friendship while blocking",
			"user", c.UserID, "blocked", target, "error", err)
	} else if err == nil {
		s.notifyFriendDeleted(r.Context(), target)
	}
	for _, pair := range [][2]string{{c.UserID, target}, {target, c.UserID}} {
		if req, err := s.store.FindPendingRequest(r.Context(), pair[0], pair[1]); err == nil {
			if err := s.store.SetFriendRequestState(r.Context(), req.ID, db.RequestCancelled, now); err != nil {
				s.log.Warn("baas: cancel friend request while blocking",
					"request", req.ID, "error", err)
			}
		}
	}

	s.log.Info("baas: blocked user", "user", c.UserID, "blocked", target)
	w.WriteHeader(http.StatusNoContent)
}

// removeBlock unblocks a user.
func (s *Server) removeBlock(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireSelf(w, r, c) {
		return
	}
	target := strings.ToLower(r.PathValue("other"))

	err := s.store.RemoveBlock(r.Context(), c.UserID, target)
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, r, errResourceNotFound)
		return
	case err != nil:
		writeInternalError(s.log, w, r, "remove block", err)
		return
	}

	s.log.Info("baas: unblocked user", "user", c.UserID, "unblocked", target)
	w.WriteHeader(http.StatusNoContent)
}
