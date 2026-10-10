// Package presencebus fans out presence changes to whatever wants them.
//
// It exists so that "user X started playing title Y" reaches the interested
// parties as an event rather than by polling the database. Phase 1 has one
// publisher (the BAAS server) and no subscriber; phase 2 adds the NPLN server
// and the dashboard, which needs a live "online now" list without hammering
// Postgres once a second per open browser tab.
package presencebus

import (
	"context"
	"encoding/json"
	"time"
)

// Event is one presence change.
type Event struct {
	// UserID is the BAAS user whose presence changed.
	UserID string `json:"userId"`
	// State is OFFLINE, INACTIVE, ONLINE or PLAYING.
	State string `json:"state"`
	// AppID is the title id the user is playing, or empty.
	AppID string `json:"appId,omitempty"`
	// PresenceGroupID groups related titles, and is usually the same as AppID.
	PresenceGroupID string `json:"presenceGroupId,omitempty"`
	// AppField is the game-chosen JSON string that carries in-game detail. It
	// is passed through untouched.
	AppField string `json:"appField,omitempty"`
	// At is when the change was observed.
	At time.Time `json:"at"`
	// Source names what reported the change, for debugging: "baas" for a
	// console's own report, "liveness" for the request-traffic heartbeat.
	Source string `json:"source"`

	// Type and Payload are set only on the notification channel (the
	// presence channel's name plus ".notify"): Type is the friends
	// sysmodule's event name, Payload the JSON npns hands it, and UserID the
	// recipient rather than whose presence changed.
	Type    string          `json:"type,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Stats is a snapshot of what a game server is doing right now.
//
// A GAUGE, not an event, and that distinction is why it is not on the event
// stream. A dashboard that has just been opened needs the current value; an
// event stream only carries changes, so a freshly-loaded page would show
// nothing until the next player queued. So this is published as a value that
// replaces the last one and is read on demand.
type Stats struct {
	// Matchmaking is how many players are waiting, per mode. Absent modes have
	// nobody queuing; an empty map means nobody is queuing at all, which is
	// different from no data — see Stats's second return value.
	Matchmaking map[string]int `json:"matchmaking"`
	// At is when the snapshot was taken. The reader uses it to decide whether
	// the publisher is still alive: a gauge with no staleness check reads as
	// "nobody is queuing" long after the game server has stopped.
	At time.Time `json:"at"`
}

// StatsTTL is how long a published snapshot stays current.
//
// Deliberately short relative to how often the game server publishes. A stale
// gauge is worse than an absent one: "nobody is queuing" and "the game server
// is not running" look identical on a tile, and only one of them is worth
// acting on.
const StatsTTL = 30 * time.Second

// Bus publishes and subscribes to presence events, and carries the game
// server's live stats.
//
// Publish must never block for long and must never fail the caller's request:
// a presence event that does not reach the dashboard is a cosmetic problem,
// while a friend-list request that fails because Redis is down is not.
type Bus interface {
	// Publish sends an event to every subscriber.
	Publish(ctx context.Context, e Event) error
	// Subscribe returns a channel of events. The channel is closed when ctx is
	// cancelled. Slow subscribers drop events rather than blocking the publisher.
	Subscribe(ctx context.Context) (<-chan Event, error)

	// PublishStats replaces the current snapshot.
	PublishStats(ctx context.Context, s Stats) error
	// Stats returns the current snapshot, and false when there is none or the
	// last one has gone stale.
	Stats(ctx context.Context) (Stats, bool, error)

	// Close releases the bus's resources.
	Close() error
}
