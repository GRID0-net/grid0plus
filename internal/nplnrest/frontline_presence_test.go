package nplnrest

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// daPresenceFrame builds a DAPresence the way npns does on FW 22.5.
func daPresenceFrame(now time.Time, status uint16, js, userID, daID string) []byte {
	return frontlineFrame(frontlineDAPresence, now, func(w *fbWriter) int {
		pos, refs := w.table([]*fbField{ref(), ref(), ref()})
		inner, irefs := w.table([]*fbField{u16(status), ref(), u16(1)})
		w.patch(refs[0], inner)
		w.patch(irefs[1], w.str(js))
		w.patch(refs[1], w.str(userID))
		w.patch(refs[2], w.str(daID))
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
}

// npns waits 10 s for an Ack carrying the id it stored, and aborts on a
// different one: "topic.subscription" for a subscribe, user/device account for
// a presence.
func TestSubscribeTopicAndDAPresenceAreAckedWithTheIdNpnsStored(t *testing.T) {
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
	da := daPresenceFrame(now, 2, "{}", "2a1a27dd90f47c89", "cba93c582facbb8a")
	if id := ackID(s.frontlineReply(da[4:], frontlineDAPresence)); id != "2a1a27dd90f47c89/cba93c582facbb8a" {
		t.Fatalf("presence ack id = %q", id)
	}
}
