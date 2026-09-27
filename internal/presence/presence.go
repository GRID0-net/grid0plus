// Package presence resolves a user's presence through GRID0+'s TTL rules.
//
// It exists so that BAAS's HTTP presence endpoints (internal/baas/presence.go)
// and NPLN's PresenceService (internal/npln) apply exactly the same rules,
// rather than risk two implementations quietly drifting apart. CLAUDE.md
// documents that divergence as the shape of one of this project's two named
// production bugs — "friends were never online" — so there is one
// implementation here and both callers delegate to it.
//
// Two rules, both learned from a deployment where they were missing:
//
//  1. A reported state expires. The console reports PLAYING when a game starts
//     and does not report again. Without a TTL, a console that was switched off
//     mid-game shows as playing forever.
//
//  2. Request traffic is proof of life. The console does not report anything
//     while it sits in the Home Menu, which is exactly where players look at
//     each other's status. But it keeps polling. Treating that traffic as an
//     ONLINE heartbeat is what makes an idle console visible.
//
// The read path takes the strongest fresh signal, and a weak signal never
// downgrades a strong one.
package presence

import (
	"context"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// Presence extras keys, as the console writes them.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#presence-extras
const (
	KeyAppID = "appInfo:appId"
	// KeyPresenceGroupID groups related titles. It is usually the same value as
	// the app id.
	KeyPresenceGroupID = "appInfo:presenceGroupId"
	// KeyACDIndex was added in system version 19.0.0.
	KeyACDIndex = "appInfo:acdIndex"
	// KeyAppField is a JSON-encoded string the game chooses. It is what makes
	// the friends applet show in-game detail, and it is passed through
	// untouched: parsing it would mean guessing at each game's private format.
	KeyAppField = "appField"
)

// Resolver applies the TTL rules against a store.
//
// A struct rather than free functions taking a store, so a caller builds one
// once from its own config and reuses it — the same shape internal/baas's
// Server already gave these methods before the extraction.
type Resolver struct {
	Store db.Store
	// ReportTTL is how long an explicitly reported state stays valid without a
	// refresh.
	ReportTTL time.Duration
	// ConsoleTTL is how long a console counts as ONLINE after its last
	// authenticated request.
	ConsoleTTL time.Duration
}

// Effective resolves one user's presence through the TTL rules.
func (r Resolver) Effective(ctx context.Context, userID string, now time.Time) (db.Presence, error) {
	p, err := r.Store.GetPresence(ctx, userID)
	if err != nil {
		return db.Presence{}, err
	}
	return Resolve(p, now, r.ReportTTL, r.ConsoleTTL), nil
}

// EffectiveMany resolves presence for several users at once.
func (r Resolver) EffectiveMany(ctx context.Context, userIDs []string, now time.Time) (map[string]db.Presence, error) {
	raw, err := r.Store.GetPresences(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]db.Presence, len(raw))
	for id, p := range raw {
		out[id] = Resolve(p, now, r.ReportTTL, r.ConsoleTTL)
	}
	return out, nil
}

// Resolve applies the TTL rules to a stored presence row.
func Resolve(p db.Presence, now time.Time, reportTTL, consoleTTL time.Duration) db.Presence {
	reportedFresh := !p.ReportedAt.IsZero() && now.Sub(p.ReportedAt) <= reportTTL
	seenFresh := !p.LastSeenAt.IsZero() && now.Sub(p.LastSeenAt) <= consoleTTL

	switch {
	case reportedFresh:
		// The console said what it is doing recently enough to believe, and it
		// knows better than any heuristic here. That includes a fresh OFFLINE: a
		// console reporting that is signing out, and overriding it with the
		// liveness heartbeat would show a user who just logged out as online.
		//
		// The app fields still have to be cleared for a state that is not
		// PLAYING, because a console reporting ONLINE after closing a game does
		// not always clear them itself.
		if p.State != db.StatePlaying {
			p.AppID = ""
			p.PresenceGroupID = ""
			p.AppField = ""
			p.Extras = ClearAppFields(p.Extras)
		}
		return p

	case seenFresh:
		// Nothing fresh was reported, but the console is talking to us. It is
		// online, and it is not in a game as far as we know — so the app fields
		// are cleared rather than kept, otherwise a friend list would show
		// "playing X" for a game that was closed an hour ago.
		//
		// This branch is the one that matters most: without it, a console idling
		// in the Home Menu reports nothing and reads as offline to all its
		// friends, which is exactly where players look at each other's status.
		p.State = db.StateOnline
		p.AppID = ""
		p.PresenceGroupID = ""
		p.AppField = ""
		p.Extras = ClearAppFields(p.Extras)
		return p

	default:
		p.State = db.StateOffline
		p.AppID = ""
		p.PresenceGroupID = ""
		p.AppField = ""
		p.Extras = ClearAppFields(p.Extras)
		return p
	}
}

// Visible reports whether owner's presence is visible to viewer, per owner's
// own privacy setting.
//
//	FRIENDS           any friend (the default)
//	FAVORITE_FRIENDS  only friends the owner marked as favourites
//	SELF              nobody
//
// The default has to be FRIENDS. SELF as a default is a friend list where
// everybody is permanently offline and nothing says why.
//
// Shared between BAAS's friend list (internal/baas/friends.go) and NPLN's
// Friends service, which both need the same answer to "may X see Y online" --
// the latter to set FriendUser.Relationship's presence_deliverable and
// presence_receivable, per docs/ROADMAP.md#2.5.
func Visible(ctx context.Context, store db.Store, owner db.User, viewerID string) bool {
	switch owner.Permissions.Presence {
	case "SELF":
		return false
	case "FAVORITE_FRIENDS":
		// The owner's own favourite flag, on the owner's side of the friendship.
		f, err := store.GetFriendship(ctx, owner.ID, viewerID)
		if err != nil {
			return false
		}
		return f.Favorite
	default:
		// FRIENDS, and anything unrecognised. A setting we do not know must not
		// silently hide presence.
		f, err := store.GetFriendship(ctx, owner.ID, viewerID)
		return err == nil && f.UserID != ""
	}
}

// ClearAppFields removes the app fields from every extras scope, leaving any
// other key alone.
func ClearAppFields(extras db.ExtrasByScope) db.ExtrasByScope {
	out := make(db.ExtrasByScope, len(db.Scopes))
	for _, scope := range db.Scopes {
		kv := map[string]any{}
		for k, v := range extras[scope] {
			switch k {
			case KeyAppID, KeyPresenceGroupID, KeyACDIndex, KeyAppField:
			default:
				kv[k] = v
			}
		}
		out[scope] = kv
	}
	return out
}
