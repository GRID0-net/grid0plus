package lobbymessaging

import (
	"testing"
	"time"
)

func TestDrainWithNoResumeTokenReturnsEverythingRetained(t *testing.T) {
	q := New(func() time.Time { return time.Unix(0, 0) })
	q.Send("lobby-1", "alice", Body{RequestID: "r1"})
	q.Send("lobby-1", "alice", Body{RequestID: "r2"})

	messages, _ := q.Drain("lobby-1", "")
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	if messages[0].Body.RequestID != "r1" || messages[1].Body.RequestID != "r2" {
		t.Errorf("wrong order: %+v", messages)
	}
}

func TestDrainAfterATokenOnlyReturnsWhatCameAfterIt(t *testing.T) {
	q := New(func() time.Time { return time.Unix(0, 0) })
	q.Send("lobby-1", "alice", Body{RequestID: "r1"})
	_, firstToken := q.Drain("lobby-1", "")

	q.Send("lobby-1", "alice", Body{RequestID: "r2"})
	messages, secondToken := q.Drain("lobby-1", firstToken)
	if len(messages) != 1 || messages[0].Body.RequestID != "r2" {
		t.Fatalf("got %+v, want only r2", messages)
	}
	if secondToken == firstToken {
		t.Error("the resume token did not advance")
	}
}

// TestLobbiesAreIndependentTopics is the property the whole package exists
// for: membership is "who is subscribed to this lobby's topic", so a
// message sent to one lobby must never reach a console tuned to a
// different one.
func TestLobbiesAreIndependentTopics(t *testing.T) {
	q := New(func() time.Time { return time.Unix(0, 0) })
	q.Send("lobby-1", "alice", Body{RequestID: "r1"})

	lobby1, _ := q.Drain("lobby-1", "")
	lobby2, _ := q.Drain("lobby-2", "")
	if len(lobby1) != 1 {
		t.Fatalf("lobby-1 got %d messages, want 1", len(lobby1))
	}
	if len(lobby2) != 0 {
		t.Errorf("lobby-2, which nothing was sent to, got %d messages", len(lobby2))
	}
}

func TestSubscribeOnlyDeliversWhatArrivesAfterIt(t *testing.T) {
	q := New(func() time.Time { return time.Unix(0, 0) })
	q.Send("lobby-1", "alice", Body{RequestID: "before"})

	events, stop := q.Subscribe("lobby-1")
	defer stop()

	q.Send("lobby-1", "alice", Body{RequestID: "after"})

	select {
	case m := <-events:
		if m.Body.RequestID != "after" {
			t.Errorf("got %q, want only messages sent after Subscribe", m.Body.RequestID)
		}
	default:
		t.Fatal("nothing arrived on the live channel")
	}
}

func TestStopUnsubscribes(t *testing.T) {
	q := New(func() time.Time { return time.Unix(0, 0) })
	events, stop := q.Subscribe("lobby-1")
	stop()

	q.Send("lobby-1", "alice", Body{RequestID: "r1"})

	select {
	case m, open := <-events:
		if open {
			t.Errorf("stopped subscriber still received %+v", m)
		}
	default:
		// Also acceptable: nothing was ever sent to the closed channel.
	}
}
