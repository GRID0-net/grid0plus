package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
)

// ---- database stats ---------------------------------------------------------

// debugStats mirrors db.Stats with JSON tags -- db.Stats itself has none,
// the same as db.User/db.Match/db.Fest and unlike db.Permissions, which is
// serialized as-is because it round-trips through a Nintendo-shaped response
// elsewhere. This one is dashboard-only, so it gets the same local wrapper
// treatment Player/festRow/matchRow already do rather than being the one
// exception in db that carries JSON tags for a single caller.
type debugStats struct {
	Users                 int `json:"users"`
	DeletedUsers          int `json:"deletedUsers"`
	DeviceAccounts        int `json:"deviceAccounts"`
	DeletedDeviceAccounts int `json:"deletedDeviceAccounts"`
	Devices               int `json:"devices"`
	Friendships           int `json:"friendships"`
	Fests                 int `json:"fests"`
	CoopShifts            int `json:"coopShifts"`
	Matches               int `json:"matches"`
	Images                int `json:"images"`
}

func (s *Server) handleDebugStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats(r.Context())
	if err != nil {
		s.fail(w, "debug stats", err)
		return
	}
	writeJSON(w, debugStats{
		Users: stats.Users, DeletedUsers: stats.DeletedUsers,
		DeviceAccounts: stats.DeviceAccounts, DeletedDeviceAccounts: stats.DeletedDeviceAccounts,
		Devices: stats.Devices, Friendships: stats.Friendships,
		Fests: stats.Fests, CoopShifts: stats.CoopShifts,
		Matches: stats.Matches, Images: stats.Images,
	})
}

// ---- account lookup ---------------------------------------------------------

// debugPresence is one presence reading -- either what a console last
// reported (RAW) or what the friends applet actually sees (EFFECTIVE),
// resolved the same way internal/presence.Resolver does everywhere else.
// Showing both side by side is the point: the two bugs CLAUDE.md documents
// this codebase around are exactly cases where they silently disagreed.
type debugPresence struct {
	State      string `json:"state"`
	AppID      string `json:"appId,omitempty"`
	ReportedAt string `json:"reportedAt,omitempty"`
	LastSeenAt string `json:"lastSeenAt,omitempty"`
	LogoutAt   string `json:"logoutAt,omitempty"`
}

// debugDeviceAccount is one credential linked to an account, including
// state playerDeviceAccount (server.go) deliberately omits: this tab exists
// to manage that state, not just read it. The password itself is still never
// included -- it is a permanent, unrotatable per-console credential, and
// the dashboard has no legitimate use for the value itself, only for being
// able to revoke or delete what it authenticates.
type debugDeviceAccount struct {
	ID                  string `json:"id"`
	CreatedAt           string `json:"createdAt"`
	DeletedAt           string `json:"deletedAt,omitempty"`
	TokensRevokedBefore string `json:"tokensRevokedBefore,omitempty"`
}

type debugAccount struct {
	ID                string               `json:"id"`
	Nickname          string               `json:"nickname"`
	Deleted           bool                 `json:"deleted"`
	CreatedAt         string               `json:"createdAt"`
	RawPresence       debugPresence        `json:"rawPresence"`
	EffectivePresence debugPresence        `json:"effectivePresence"`
	DeviceAccounts    []debugDeviceAccount `json:"deviceAccounts"`
}

func (s *Server) handleDebugAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	u, err := s.store.GetUser(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "no such account", http.StatusNotFound)
			return
		}
		s.fail(w, "debug: load account", err)
		return
	}

	raw, err := s.store.GetPresence(ctx, id)
	if err != nil {
		s.fail(w, "debug: load raw presence", err)
		return
	}
	now := s.now()
	eff, err := s.presence.Effective(ctx, id, now)
	if err != nil {
		s.fail(w, "debug: resolve presence", err)
		return
	}

	das, err := s.store.ListAllDeviceAccounts(ctx, id)
	if err != nil {
		s.fail(w, "debug: list device accounts", err)
		return
	}

	detail := debugAccount{
		ID:                u.ID,
		Nickname:          u.Nickname,
		Deleted:           u.Deleted,
		CreatedAt:         u.CreatedAt.UTC().Format(time.RFC3339),
		RawPresence:       presenceView(raw),
		EffectivePresence: presenceView(eff),
	}
	detail.DeviceAccounts = make([]debugDeviceAccount, 0, len(das))
	for _, da := range das {
		row := debugDeviceAccount{ID: da.ID, CreatedAt: da.CreatedAt.UTC().Format(time.RFC3339)}
		if da.DeletedAt != nil {
			row.DeletedAt = da.DeletedAt.UTC().Format(time.RFC3339)
		}
		if da.TokensRevokedBefore != nil {
			row.TokensRevokedBefore = da.TokensRevokedBefore.UTC().Format(time.RFC3339)
		}
		detail.DeviceAccounts = append(detail.DeviceAccounts, row)
	}

	writeJSON(w, detail)
}

func presenceView(p db.Presence) debugPresence {
	v := debugPresence{State: p.State, AppID: p.AppID}
	if !p.ReportedAt.IsZero() {
		v.ReportedAt = p.ReportedAt.UTC().Format(time.RFC3339)
	}
	if !p.LastSeenAt.IsZero() {
		v.LastSeenAt = p.LastSeenAt.UTC().Format(time.RFC3339)
	}
	if p.LogoutAt != nil {
		v.LogoutAt = p.LogoutAt.UTC().Format(time.RFC3339)
	}
	return v
}

// ---- account controls ---------------------------------------------------------

// handleDebugSetAccountDeleted toggles db.User.Deleted -- the same fail-closed
// flag internal/baas's login path already checks (TODO.md §6.1), not a
// separate ban list. Reversible: setting it back to false is enough to let
// the account log in again, nothing else to undo.
func (s *Server) handleDebugSetAccountDeleted(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Deleted bool `json:"deleted"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if _, err := s.store.UpdateUser(r.Context(), id, db.UserPatch{Deleted: &body.Deleted}); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "no such account", http.StatusNotFound)
			return
		}
		s.fail(w, "debug: set account deleted", err)
		return
	}
	s.log.Info("dashboard: account deleted flag set", "user", id, "deleted", body.Deleted)
	writeJSON(w, map[string]any{"id": id, "deleted": body.Deleted})
}

// handleDebugRevokeDeviceAccountTokens invalidates every token already
// minted for one device account, without touching its password -- the
// console re-authenticates on its next request rather than being logged
// out of nothing to log back into.
func (s *Server) handleDebugRevokeDeviceAccountTokens(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("deviceAccountId")
	if err := s.store.RevokeDeviceAccountTokens(r.Context(), id, s.now()); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "no such device account", http.StatusNotFound)
			return
		}
		s.fail(w, "debug: revoke device account tokens", err)
		return
	}
	s.log.Info("dashboard: device account tokens revoked", "deviceAccount", id)
	writeJSON(w, map[string]any{"id": id, "revokedAt": s.now().UTC().Format(time.RFC3339)})
}

// handleDebugSetDeviceAccountDeleted deletes or undeletes one device
// account. Both directions exist because the console-facing path
// (internal/baas) only ever soft-deletes -- see db.Store's
// UndeleteDeviceAccount doc.
func (s *Server) handleDebugSetDeviceAccountDeleted(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	daID := r.PathValue("deviceAccountId")
	var body struct {
		Deleted bool `json:"deleted"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var err error
	if body.Deleted {
		err = s.store.DeleteDeviceAccount(r.Context(), userID, daID)
	} else {
		err = s.store.UndeleteDeviceAccount(r.Context(), daID)
	}
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "no such device account", http.StatusNotFound)
			return
		}
		s.fail(w, "debug: set device account deleted", err)
		return
	}
	s.log.Info("dashboard: device account deleted flag set", "deviceAccount", daID, "deleted", body.Deleted)
	writeJSON(w, map[string]any{"id": daID, "deleted": body.Deleted})
}
