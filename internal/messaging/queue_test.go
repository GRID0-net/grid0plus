package messaging

import (
	"testing"
	"time"
)

func TestDrainWithNoResumeTokenReturnsEverythingRetained(t *testing.T) {
	q := New(func() time.Time { return time.Unix(0, 0) })
	q.Send("alice", []string{"bob"}, Body{RequestID: "r1"})
	q.Send("alice", []string{"bob"}, Body{RequestID: "r2"})

	messages, _, _, _ := q.Drain("bob", "", "")
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	if messages[0].Body.RequestID != "r1" || messages[1].Body.RequestID != "r2" {
		t.Errorf("wrong order: %+v", messages)
	}
}

func TestDrainAfterATokenOnlyReturnsWhatCameAfterIt(t *testing.T) {
	q := New(func() time.Time { return time.Unix(0, 0) })
	q.Send("alice", []string{"bob"}, Body{RequestID: "r1"})
	_, _, firstToken, _ := q.Drain("bob", "", "")

	q.Send("alice", []string{"bob"}, Body{RequestID: "r2"})
	messages, _, secondToken, _ := q.Drain("bob", firstToken, "")
	if len(messages) != 1 || messages[0].Body.RequestID != "r2" {
		t.Fatalf("got %+v, want only r2", messages)
	}
	if secondToken == firstToken {
		t.Error("the resume token did not advance")
	}
}

func TestSendFansOutToEveryReceiverIndependently(t *testing.T) {
	q := New(func() time.Time { return time.Unix(0, 0) })
	q.Send("alice", []string{"bob", "carol"}, Body{RequestID: "r1"})

	bobMsgs, _, _, _ := q.Drain("bob", "", "")
	carolMsgs, _, _, _ := q.Drain("carol", "", "")
	if len(bobMsgs) != 1 || len(carolMsgs) != 1 {
		t.Fatalf("bob=%d carol=%d, want 1 each", len(bobMsgs), len(carolMsgs))
	}

	// A third party who was not named gets nothing.
	daveMsgs, _, _, _ := q.Drain("dave", "", "")
	if len(daveMsgs) != 0 {
		t.Errorf("dave, who was not a receiver, got %d messages", len(daveMsgs))
	}
}

// TestAckRoutesToTheOriginalSenderNotTheAcker is the routing rule the whole
// ack path depends on: SendAck names who is acking (the caller) and who sent
// the original message (carried in the ack body), and the acknowledgement has
// to land in the ORIGINAL SENDER's mailbox, not the acker's own.
func TestAckRoutesToTheOriginalSenderNotTheAcker(t *testing.T) {
	q := New(func() time.Time { return time.Unix(0, 0) })
	q.Ack("bob", "r1", "alice")

	_, aliceAcks, _, _ := q.Drain("alice", "", "")
	if len(aliceAcks) != 1 {
		t.Fatalf("alice (the original sender) got %d acks, want 1", len(aliceAcks))
	}
	if aliceAcks[0].AckedByID != "bob" {
		t.Errorf("acked_by = %q, want bob", aliceAcks[0].AckedByID)
	}

	_, bobAcks, _, _ := q.Drain("bob", "", "")
	if len(bobAcks) != 0 {
		t.Errorf("bob (who sent the ack) has %d acks in his own mailbox, want 0", len(bobAcks))
	}
}

func TestMailboxDepthIsBoundedDroppingTheOldest(t *testing.T) {
	q := New(func() time.Time { return time.Unix(0, 0) })
	for i := 0; i < maxMailboxDepth+10; i++ {
		q.Send("alice", []string{"bob"}, Body{RequestID: FormatToken(uint64(i))})
	}
	messages, _, _, _ := q.Drain("bob", "", "")
	if len(messages) != maxMailboxDepth {
		t.Fatalf("got %d messages, want the cap of %d", len(messages), maxMailboxDepth)
	}
	// The oldest ten were dropped, so the first retained one is #10.
	if messages[0].Body.RequestID != FormatToken(10) {
		t.Errorf("oldest retained message has RequestID %q, want %q",
			messages[0].Body.RequestID, FormatToken(10))
	}
}

// TestSubscribeOnlyDeliversWhatArrivesAfterItIsCalled is the contract Drain
// and Subscribe together provide: Drain for what already happened, Subscribe
// for what happens next -- callers must call Drain first or lose nothing in
// between, and this proves Subscribe alone would.
func TestSubscribeDeliversLiveArrivals(t *testing.T) {
	q := New(func() time.Time { return time.Unix(0, 0) })
	events, stop := q.Subscribe("bob")
	defer stop()

	q.Send("alice", []string{"bob"}, Body{RequestID: "live"})

	select {
	case e := <-events:
		m, ok := e.(Message)
		if !ok || m.Body.RequestID != "live" {
			t.Fatalf("got %#v, want the live message", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event delivered")
	}
}

func TestSubscribeStopRemovesTheSubscriber(t *testing.T) {
	q := New(func() time.Time { return time.Unix(0, 0) })
	_, stop := q.Subscribe("bob")
	stop()

	b := q.box("bob")
	if len(b.subs) != 0 {
		t.Errorf("%d subscribers remain after stop", len(b.subs))
	}
}
