package nplnrest

import (
	"encoding/hex"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// daPresenceFrame builds a DAPresence the way npns does on FW 22.5.
func daPresenceFrame(now time.Time, status uint16, js, userID, daID string) []byte {
	return frontlineFrame(frontlineDAPresence, now, func(w *fbWriter) int {
		pos, refs := w.table([]*fbField{ref(), ref(), ref()})
		inner, irefs := w.table([]*fbField{u16(status), ref(), u16(1)})
		w.patch(refs[2], inner)
		w.patch(irefs[1], w.str(js))
		w.patch(refs[1], w.str(userID))
		w.patch(refs[0], w.str(daID))
		return pos
	})
}

// A console on FW 22.5 never PATCHes its presence to BAAS; it sends a
// DAPresence on the frontline, and that is what must make it read as playing.
func TestAConsolesFrontlinePresenceMakesItPlaying(t *testing.T) {
	ctx := t.Context()
	store := db.NewMemory()
	now := time.Date(2026, 10, 10, 14, 0, 0, 0, time.UTC)
	for _, id := range []string{"2a1a27dd90f47c89", "26baf7658281f2b1"} {
		if err := store.CreateUser(ctx, db.User{ID: id, Nickname: id[:1], CreatedAt: now}, db.DeviceAccount{ID: "da" + id[2:]}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), FrontlineHost: frontlineHost,
		Now: func() time.Time { return now }, Store: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	const js = `{"self":{},"favoriteFriends":{},"friends":{"appField":"{}","appInfo:appId":"0100c2500fc20000","appInfo:acdIndex":0,"appInfo:presenceGroupId":"0100c2500fc20000"},"foaf":{},"everyone":{}}`

	// Another user's device account is refused: identity is fail-closed.
	frame := daPresenceFrame(now, 2, js, "2a1a27dd90f47c89", "da"+"baf7658281f2b1")
	if _, err := s.recordDAPresence(ctx, frame[4:]); err == nil {
		t.Fatal("a DAPresence naming someone else's device account was accepted")
	}
	if p, _ := store.GetPresence(ctx, "2a1a27dd90f47c89"); p.State == db.StatePlaying {
		t.Fatal("refused presence was stored")
	}

	frame = daPresenceFrame(now, 2, js, "2a1a27dd90f47c89", "da"+"1a27dd90f47c89")
	if _, err := s.recordDAPresence(ctx, frame[4:]); err != nil {
		t.Fatal(err)
	}
	p, err := store.GetPresence(ctx, "2a1a27dd90f47c89")
	if err != nil {
		t.Fatal(err)
	}
	if p.State != db.StatePlaying || p.AppID != "0100c2500fc20000" || p.PresenceGroupID != "0100c2500fc20000" || p.AppField != "{}" {
		t.Fatalf("presence = %+v", p)
	}
	// A friend's console reads the game only from extras.friends.
	if got := p.Extras[db.ScopeFriends]["appInfo:appId"]; got != "0100c2500fc20000" {
		t.Fatalf("extras.friends appInfo:appId = %v, want the game; extras = %v", got, p.Extras)
	}

	// In a game npns mostly says status 1 with the game's id: still playing.
	frame = daPresenceFrame(now, 1, js, "2a1a27dd90f47c89", "da"+"1a27dd90f47c89")
	if _, err := s.recordDAPresence(ctx, frame[4:]); err != nil {
		t.Fatal(err)
	}
	if p, _ = store.GetPresence(ctx, "2a1a27dd90f47c89"); p.State != db.StatePlaying {
		t.Fatalf("status 1 in a game: state %s, want PLAYING", p.State)
	}

	// The Home Menu reports an all-zero id: online, not playing.
	home := strings.ReplaceAll(js, "0100c2500fc20000", "0000000000000000")
	frame = daPresenceFrame(now, 1, home, "2a1a27dd90f47c89", "da"+"1a27dd90f47c89")
	if _, err := s.recordDAPresence(ctx, frame[4:]); err != nil {
		t.Fatal(err)
	}
	if p, _ = store.GetPresence(ctx, "2a1a27dd90f47c89"); p.State != db.StateOnline {
		t.Fatalf("Home Menu: state %s, want ONLINE", p.State)
	}

	// Closing the game: the next report has no app keys, and none linger.
	frame = daPresenceFrame(now, 1, `{"self":{},"favoriteFriends":{},"friends":{},"foaf":{},"everyone":{}}`, "2a1a27dd90f47c89", "da"+"1a27dd90f47c89")
	if _, err := s.recordDAPresence(ctx, frame[4:]); err != nil {
		t.Fatal(err)
	}
	p, _ = store.GetPresence(ctx, "2a1a27dd90f47c89")
	if _, ok := p.Extras[db.ScopeFriends]["appInfo:appId"]; ok || p.State != db.StateOnline {
		t.Fatalf("after closing the game: state %s, extras %v", p.State, p.Extras)
	}
}

// npns waits 10 s for an Ack carrying the id it stored, and aborts on a
// different one: "topic.subscription" for a subscribe, user/device account for
// a presence.
func TestSubscribeTopicPowerStateAndDAPresenceAreAckedWithTheIdNpnsStored(t *testing.T) {
	s := newServer(t, t.TempDir())
	now := time.Unix(1, 0)
	ackID := func(reply []byte) string {
		t.Helper()
		if frontlineFrameType(reply[4:]) != frontlineAck {
			t.Fatalf("reply type = %d, want Ack", frontlineFrameType(reply[4:]))
		}
		body, _ := frontlineBody(reply[4:])
		slot, ok := fbFieldPos(reply[4:], body, 0)
		if !ok {
			t.Fatal("Ack has no id")
		}
		return fbString(reply[4:], slot)
	}
	sub := frontlineFrame(frontlineSubscribeTopic, now, func(w *fbWriter) int {
		pos, refs := w.table([]*fbField{nil, ref(), ref()})
		w.patch(refs[1], w.str(""))
		w.patch(refs[2], w.str("nx_data_0100c2500fc20000"))
		return pos
	})
	if id := ackID(s.frontlineReply(sub[4:], frontlineSubscribeTopic)); id != "topic.subscription" {
		t.Fatalf("subscribe ack id = %q", id)
	}
	for v, want := range map[uint16]string{0: "Awake", 1: "Sleep"} {
		ps := frontlineFrame(frontlinePowerState, now, func(w *fbWriter) int {
			fields := []*fbField{u16(v)}
			if v == 0 {
				fields = nil // npns leaves a default out, as on hardware
			}
			pos, _ := w.table(fields)
			return pos
		})
		if id := ackID(s.frontlineReply(ps[4:], frontlinePowerState)); id != want {
			t.Fatalf("power state %d ack id = %q, want %q", v, id, want)
		}
	}
	da := daPresenceFrame(now, 2, "{}", "2a1a27dd90f47c89", "cba93c582facbb8a")
	if id := ackID(s.frontlineReply(da[4:], frontlineDAPresence)); id != "2a1a27dd90f47c89/cba93c582facbb8a" {
		t.Fatalf("presence ack id = %q", id)
	}
}

// The frame npns sends, byte for byte: the device account is field 0 and the
// presence table field 2. Reading them the other way round acks a garbage id,
// which npns aborts on.
func TestAHardwareDAPresenceIsReadFieldForField(t *testing.T) {
	frame, _ := hex.DecodeString("180000000000000000000e002400070008000c00100018000e0000000000001b30000000180000008349ca6a000000009357ca6a0000000000000000020000004e58000000000a001200040008000c000a000000fc000000e00000001000000000000a000c000400080006000a0000000200010004000000b90000007b2273656c66223a7b7d2c226661766f72697465467269656e6473223a7b7d2c22667269656e6473223a7b226170704669656c64223a227b7d222c22617070496e666f3a6170704964223a2230313030633235303066633230303030222c22617070496e666f3a616364496e646578223a302c22617070496e666f3a70726573656e636547726f75704964223a2230313030633235303066633230303030227d2c22666f6166223a7b7d2c2265766572796f6e65223a7b7d7d000000100000003261316132376464393066343763383900000000100000006362613933633538326661636262386100000000")
	d, err := parseDAPresence(frame)
	if err != nil {
		t.Fatal(err)
	}
	if d.UserID != "2a1a27dd90f47c89" || d.DeviceAccountID != "cba93c582facbb8a" || d.Status != 2 {
		t.Fatalf("parsed %+v", d)
	}
	if !strings.Contains(d.JSON, `"appInfo:appId":"0100c2500fc20000"`) {
		t.Fatalf("json = %s", d.JSON)
	}
}

// A deleted user's console kept reporting presence on its frontline, and
// friends kept seeing it online.
func TestADeletedUsersFrontlinePresenceIsRefused(t *testing.T) {
	ctx := t.Context()
	store := db.NewMemory()
	now := time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)
	const id = "2a1a27dd90f47c89"
	if err := store.CreateUser(ctx, db.User{ID: id, Nickname: "x", CreatedAt: now}, db.DeviceAccount{ID: "da" + id[2:]}); err != nil {
		t.Fatal(err)
	}
	deleted := true
	if _, err := store.UpdateUser(ctx, id, db.UserPatch{Deleted: &deleted}); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), FrontlineHost: frontlineHost,
		Now: func() time.Time { return now }, Store: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := daPresenceFrame(now, 1, `{"self":{},"favoriteFriends":{},"friends":{},"foaf":{},"everyone":{}}`, id, "da"+id[2:])
	if _, err := s.recordDAPresence(ctx, frame[4:]); err == nil {
		t.Fatal("a deleted user's presence was accepted")
	}
}
