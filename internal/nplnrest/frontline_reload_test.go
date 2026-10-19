package nplnrest

import (
	"encoding/hex"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestAReloadLinkIsAckedWithItsOwnId: npns waits 10 s for an Ack naming the
// "reload_link-<hex>" it sent and drops the connection otherwise.
func TestAReloadLinkIsAckedWithItsOwnId(t *testing.T) {
	frame, _ := hex.DecodeString("180000000000000000000e002400070008000c00100018000e0000000000001e2c000000180000009b94cb6a00000000aba2cb6a0000000000000000020000004e580000000006000800040006000000040000001400000072656c6f61645f6c696e6b2d323432393266366400000000")
	s, err := New(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), FrontlineHost: frontlineHost,
		Now: func() time.Time { return time.Unix(1791000000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if got := frontlineFrameType(frame); got != frontlineReloadLink {
		t.Fatalf("frame type %d, want ReloadLink", got)
	}
	reply := s.frontlineReply(frame, frontlineReloadLink)
	if reply == nil {
		t.Fatal("ReloadLink was left unanswered")
	}
	body, ok := frontlineBody(reply[4:])
	if !ok || frontlineFrameType(reply[4:]) != frontlineAck {
		t.Fatalf("reply is not an Ack: %x", reply)
	}
	slot, _ := fbFieldPos(reply[4:], body, 0)
	if id := fbString(reply[4:], slot); id != "reload_link-24292f6d" {
		t.Fatalf("Ack id %q, want reload_link-24292f6d", id)
	}
}
