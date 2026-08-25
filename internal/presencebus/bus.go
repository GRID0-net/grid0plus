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
}

// Bus publishes and subscribes to presence events.
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
	// Close releases the bus's resources.
	Close() error
}
