package npln

import (
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	commonpb "github.com/n-popescu/switchnet/proto/common"
)

// presenceAttributes holds the game-defined presence attributes each player
// publishes over PresenceService.KeepAlive, so SubscribePresences can hand
// them to that player's friends.
//
// Settled by a real Nintendo capture, recorded in NextendoNetwork/splatoon-3's
// own notes (2026-08-15; facts only, their code is not used here): a
// Splatoon 3 friend's presence carries thirteen attributes, and two of them
// drive the in-game friend list. GameStatus=1 with an empty SessionId is "just
// online"; GameStatus=2 with a non-empty SessionId is "in a room", which is
// what makes "Join" appear -- and that SessionId is the game session the
// joiner then passes to JoinGameSession. Without these, a friend who is
// playing reads as merely online, which is the bug this type exists for.
//
// Separate from db.Presence on purpose. That row has one writer, BAAS's
// console presence report, and the resolver's TTL rules are built around it;
// these attributes are NPLN's own, only mean anything to the title that wrote
// them, and live exactly as long as the KeepAlive stream that carries them.
// In memory for the same reason matchmaking's queue is: a restart ends every
// stream, and every console republishes on reconnect.
type presenceAttributes struct {
	mu       sync.Mutex
	byUser   map[string]map[string]*commonpb.Value
	streams  map[string]int
	watchers map[chan string]struct{}
}

func newPresenceAttributes() *presenceAttributes {
	return &presenceAttributes{
		byUser:   map[string]map[string]*commonpb.Value{},
		streams:  map[string]int{},
		watchers: map[chan string]struct{}{},
	}
}

// maxPresenceAttributes caps how many keys one player may hold. A real
// console sends thirteen; the cap stops a misbehaving client growing the map
// without bound, since every key is re-sent to every friend.
const maxPresenceAttributes = 64

// open records a KeepAlive stream for userID. The attributes are only
// cleared when the last of a user's streams closes: a console reconnecting
// opens the new stream before the old one is torn down, and clearing on the
// old one's close would wipe what the new one just published.
func (a *presenceAttributes) open(userID string) {
	a.mu.Lock()
	a.streams[userID]++
	a.mu.Unlock()
}

// close ends one of userID's streams, clearing the user's attributes if it
// was the last. A player who quit the game must not keep showing "in a room"
// to their friends.
func (a *presenceAttributes) close(userID string) {
	a.mu.Lock()
	a.streams[userID]--
	cleared := false
	if a.streams[userID] <= 0 {
		delete(a.streams, userID)
		if len(a.byUser[userID]) > 0 {
			cleared = true
		}
		delete(a.byUser, userID)
	}
	a.mu.Unlock()
	if cleared {
		a.notify(userID)
	}
}

// merge applies one UpdatePresence to what is already known about userID.
//
// Merged, never replaced: the console sends only the keys that changed. The
// same capture shows a player opening a room sending thirteen keys, then four
// (GameStatus, SessionId...) twenty seconds later, then one
// (CurrentParticipants) two seconds after that -- replacing the map on each
// update would erase GameStatus and SessionId the moment the participant
// count moved, and friends would never see the room.
//
// A mask path naming a key the update does not carry deletes that key, which
// is what a FieldMask means for a map entry.
//
// Reports whether anything visible changed, so a no-op republish does not
// wake every friend's stream.
func (a *presenceAttributes) merge(userID string, attrs map[string]*commonpb.Value, mask []string) bool {
	a.mu.Lock()
	current := a.byUser[userID]
	if current == nil {
		current = map[string]*commonpb.Value{}
		a.byUser[userID] = current
	}
	changed := false
	for k, v := range attrs {
		if old, ok := current[k]; ok && proto.Equal(old, v) {
			continue
		}
		if _, ok := current[k]; !ok && len(current) >= maxPresenceAttributes {
			continue
		}
		current[k] = proto.Clone(v).(*commonpb.Value)
		changed = true
	}
	for _, path := range mask {
		key, ok := strings.CutPrefix(path, "attributes.")
		if !ok {
			continue
		}
		if _, sent := attrs[key]; sent {
			continue
		}
		if _, held := current[key]; held {
			delete(current, key)
			changed = true
		}
	}
	a.mu.Unlock()
	if changed {
		a.notify(userID)
	}
	return changed
}

// get returns a copy of userID's attributes, or nil if there are none. A
// copy because merge mutates the held map while a stream may be marshalling
// the one it was handed.
func (a *presenceAttributes) get(userID string) map[string]*commonpb.Value {
	a.mu.Lock()
	defer a.mu.Unlock()
	src := a.byUser[userID]
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]*commonpb.Value, len(src))
	for k, v := range src {
		out[k] = proto.Clone(v).(*commonpb.Value)
	}
	return out
}

// watch returns a channel that receives the id of every user whose
// attributes change, and a function that stops it.
func (a *presenceAttributes) watch() (<-chan string, func()) {
	ch := make(chan string, 64)
	a.mu.Lock()
	a.watchers[ch] = struct{}{}
	a.mu.Unlock()
	return ch, func() {
		a.mu.Lock()
		delete(a.watchers, ch)
		a.mu.Unlock()
	}
}

// notify never blocks: a stream too slow to drain its channel misses a wake-up
// rather than stalling every other player's update. The buffer is sized so
// that only a stream stuck for dozens of changes loses one.
func (a *presenceAttributes) notify(userID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for ch := range a.watchers {
		select {
		case ch <- userID:
		default:
		}
	}
}
