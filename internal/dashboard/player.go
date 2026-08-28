package dashboard

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
)

// The per-player view: everything SwitchNet knows about one person, in one
// place.
//
// This is the view an operator opens when somebody says "it is not working for
// me". The useful answer is almost always a combination of two facts — a
// presence that says offline while last_seen_at is recent, a device account that
// was reassigned, a friend list with one entry — and having them on separate
// pages is what makes that combination hard to see.

// playerDetail is the whole of what is known about one player.
type playerDetail struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname"`
	Country  string `json:"country,omitempty"`

	// State is the EFFECTIVE state, resolved the same way BAAS resolves it.
	State    string `json:"state"`
	Activity string `json:"activity,omitempty"`
	AppID    string `json:"appId,omitempty"`

	// The three presence timestamps, kept apart deliberately.
	//
	// "Reported OFFLINE" and "never reported" are different states, and
	// conflating them is what makes a friend list permanently offline. Showing
	// all three is what lets an operator see which case they are in without
	// reading the database.
	ReportedState string `json:"reportedState,omitempty"`
	ReportedAt    string `json:"reportedAt,omitempty"`
	LastSeenAt    string `json:"lastSeenAt,omitempty"`

	FriendCode string `json:"friendCode,omitempty"`

	DeviceAccounts []deviceAccountRow `json:"deviceAccounts"`
	Friends        []friendRow        `json:"friends"`
	Matches        []matchRow         `json:"matches"`
}

type deviceAccountRow struct {
	ID string `json:"id"`
	// Deleted matters: a console whose device account was deleted keeps working
	// until its token expires, and this is where that shows up.
	Deleted   bool   `json:"deleted"`
	CreatedAt string `json:"createdAt,omitempty"`
}

type friendRow struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname"`
	State    string `json:"state"`
	Favorite bool   `json:"favorite"`
	Since    string `json:"since,omitempty"`
}

// handlePlayer serves one player's detail.
func (s *Server) handlePlayer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	user, err := s.store.GetUser(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "no such player", http.StatusNotFound)
			return
		}
		s.fail(w, "load player", err)
		return
	}

	now := s.now()
	presence, err := s.store.GetPresence(ctx, id)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		s.fail(w, "load presence", err)
		return
	}
	state, appID := s.resolvePresence(presence, now)

	detail := playerDetail{
		ID:             user.ID,
		Nickname:       user.Nickname,
		Country:        user.Country,
		State:          state,
		AppID:          appID,
		ReportedState:  presence.State,
		DeviceAccounts: []deviceAccountRow{},
		Friends:        []friendRow{},
		Matches:        []matchRow{},
	}
	if appID != "" {
		detail.Activity = titleName(appID)
	}
	if !presence.ReportedAt.IsZero() {
		detail.ReportedAt = presence.ReportedAt.UTC().Format(time.RFC3339)
	}
	if !presence.LastSeenAt.IsZero() {
		detail.LastSeenAt = presence.LastSeenAt.UTC().Format(time.RFC3339)
	}

	if code, err := s.store.GetFriendCode(ctx, id); err == nil {
		detail.FriendCode = code.Code
	} else if !errors.Is(err, db.ErrNotFound) {
		s.log.Debug("dashboard: load friend code", "user", id, "error", err)
	}

	accounts, err := s.store.ListDeviceAccounts(ctx, id)
	if err != nil {
		s.fail(w, "list device accounts", err)
		return
	}
	for _, a := range accounts {
		row := deviceAccountRow{ID: a.ID, Deleted: a.DeletedAt != nil}
		if !a.CreatedAt.IsZero() {
			row.CreatedAt = a.CreatedAt.UTC().Format(time.RFC3339)
		}
		detail.DeviceAccounts = append(detail.DeviceAccounts, row)
	}

	friends, err := s.store.ListFriends(ctx, id, 300)
	if err != nil {
		s.fail(w, "list friends", err)
		return
	}
	if len(friends) > 0 {
		ids := make([]string, 0, len(friends))
		for _, f := range friends {
			ids = append(ids, f.FriendID)
		}
		presences, err := s.store.GetPresences(ctx, ids)
		if err != nil {
			s.fail(w, "load friend presence", err)
			return
		}
		names := s.nicknames(ctx)

		for _, f := range friends {
			// The same presence rule as everywhere else. A friend list here
			// that disagreed with the console's would send the operator hunting
			// for a bug that is only in this page.
			friendState, _ := s.resolvePresence(presences[f.FriendID], now)
			row := friendRow{
				ID:       f.FriendID,
				Nickname: displayName(names, f.FriendID),
				State:    friendState,
				Favorite: f.Favorite,
			}
			if !f.BecameFriendAt.IsZero() {
				row.Since = f.BecameFriendAt.UTC().Format(time.RFC3339)
			}
			detail.Friends = append(detail.Friends, row)
		}
		// Favourites first, then online, then by name -- the same ordering
		// instinct as the players list: what the operator is looking for should
		// not be buried alphabetically.
		rank := map[string]int{"PLAYING": 0, "ONLINE": 1, "OFFLINE": 2}
		sort.SliceStable(detail.Friends, func(i, j int) bool {
			a, b := detail.Friends[i], detail.Friends[j]
			if a.Favorite != b.Favorite {
				return a.Favorite
			}
			if ra, rb := rank[a.State], rank[b.State]; ra != rb {
				return ra < rb
			}
			return a.Nickname < b.Nickname
		})
	}

	matches, err := s.store.ListMatches(ctx, 200)
	if err != nil {
		s.fail(w, "list matches", err)
		return
	}
	names := s.nicknames(ctx)
	for _, m := range matches {
		if !played(m, id) {
			continue
		}
		row := matchRow{
			ID:        m.ID,
			Mode:      m.Mode,
			Host:      displayName(names, m.Host),
			CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
		}
		for _, p := range m.Players {
			row.Players = append(row.Players, displayName(names, p))
		}
		detail.Matches = append(detail.Matches, row)
		// Bounded: this is a detail page, not an archive, and the store's own
		// limit is about how far back to look rather than how much to show.
		if len(detail.Matches) == 20 {
			break
		}
	}

	writeJSON(w, detail)
}

func played(m db.Match, userID string) bool {
	for _, p := range m.Players {
		if p == userID {
			return true
		}
	}
	return false
}
