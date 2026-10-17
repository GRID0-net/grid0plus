package presencebus

import (
	"context"
	"encoding/json"
	"time"
)

// FestChannelSuffix names the channel fest entries travel on, after the
// presence channel's own name, the same way ".notify" does for pushes.
//
// Its own channel because its one subscriber, the bot API, wants nothing
// else: on the presence channel every console's heartbeat would wake it.
const FestChannelSuffix = ".fest"

// FestEntryType is the Event.Type of a new fest entry.
const FestEntryType = "fest_entry"

// FestEntry is the Payload of a FestEntryType event. UserID on the Event is
// the player.
type FestEntry struct {
	FestID string `json:"festId"`
	Team   string `json:"team"`
}

// PublishFestEntry announces that a player picked a team, without making
// the caller wait on it: it runs on a console's request, and a role the
// Discord bot gives a little late (or on its next reconnect) is better than a
// vote that hangs because Redis is slow. A nil bus is a no-op.
func PublishFestEntry(bus Bus, source, festID, userID, team string, onErr func(error)) {
	if bus == nil {
		return
	}
	payload, err := json.Marshal(FestEntry{FestID: festID, Team: team})
	if err != nil {
		onErr(err)
		return
	}
	e := Event{UserID: userID, Type: FestEntryType, Payload: payload, At: time.Now().UTC(), Source: source}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := bus.Publish(ctx, e); err != nil && onErr != nil {
			onErr(err)
		}
	}()
}
