package npln

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	friendspb "github.com/n-popescu/grid0plus/proto/friends/v1"
)

type countingPresenceStore struct {
	db.Store
	writes atomic.Int64
	fail   bool
}

func (s *countingPresenceStore) TouchPresence(ctx context.Context, id string, at time.Time) error {
	s.writes.Add(1)
	if s.fail {
		return errors.New("temporary database failure")
	}
	return s.Store.TouchPresence(ctx, id, at)
}

func TestRapidKeepAlivePingsAreAckedWithoutAWritePerPing(t *testing.T) {
	h := newHarness(t)
	h.createUser("heartbeat000001", "device-heartbeat")
	token := h.sessionFor(t, "heartbeat000001", "device-heartbeat")
	store := &countingPresenceStore{Store: h.srv.store}
	h.srv.store = store
	ctx, cancel := context.WithTimeout(h.ctx(token, ""), 3*time.Second)
	defer cancel()
	stream, err := friendspb.NewPresenceServiceClient(h.conn).KeepAlive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if err := stream.Send(&friendspb.KeepAliveRequest{Request: &friendspb.KeepAliveRequest_Ack{Ack: &friendspb.Ack{}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Recv(); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.writes.Load(); got != 2 {
		t.Fatalf("12 acknowledged pings caused %d writes, want stream-open and first-ping writes", got)
	}
}

func TestHeartbeatWriteGateRespectsShortTTLAndRetriesFailures(t *testing.T) {
	h := newHarness(t)
	h.createUser("heartbeat000002", "device-heartbeat2")
	store := &countingPresenceStore{Store: h.srv.store}
	h.srv.store = store
	h.srv.presence.ConsoleTTL = time.Second
	p := &presenceService{s: h.srv}
	var last time.Time
	at := time.Now()
	p.touchHeartbeat(context.Background(), "heartbeat000002", &last, at)
	p.touchHeartbeat(context.Background(), "heartbeat000002", &last, at.Add(249*time.Millisecond))
	p.touchHeartbeat(context.Background(), "heartbeat000002", &last, at.Add(250*time.Millisecond))
	if got := store.writes.Load(); got != 2 {
		t.Fatalf("short-TTL writes = %d", got)
	}
	store.fail = true
	p.touchHeartbeat(context.Background(), "heartbeat000002", &last, at.Add(time.Second))
	store.fail = false
	p.touchHeartbeat(context.Background(), "heartbeat000002", &last, at.Add(time.Second))
	if got := store.writes.Load(); got != 4 {
		t.Fatalf("failed heartbeat suppressed retry: %d writes", got)
	}
}
