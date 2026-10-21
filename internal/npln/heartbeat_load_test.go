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

// Ash read as merely online mid-match: the console reports PLAYING once, at
// launch, and the report went stale while the game kept its stream open.
func TestTheKeepAliveKeepsAPlayingSplatoonReportFreshButNeverInventsOne(t *testing.T) {
	h := newHarness(t)
	h.createUser("heartbeat000003", "device-heartbeat3")
	h.createUser("heartbeat000004", "device-heartbeat4")
	ctx := context.Background()
	old := h.srv.now().Add(-time.Hour)
	playing, online, app := db.StatePlaying, db.StateOnline, splatoon3AppID
	if _, err := h.srv.store.UpdatePresence(ctx, "heartbeat000003", db.PresencePatch{State: &playing, AppID: &app, ReportedAt: &old}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.srv.store.UpdatePresence(ctx, "heartbeat000004", db.PresencePatch{State: &online, ReportedAt: &old}); err != nil {
		t.Fatal(err)
	}
	p := &presenceService{s: h.srv}
	var a, b time.Time
	p.touchHeartbeat(ctx, "heartbeat000003", &a, time.Now())
	p.touchHeartbeat(ctx, "heartbeat000004", &b, time.Now())

	got, err := h.srv.presence.Effective(ctx, "heartbeat000003", h.srv.now())
	if err != nil || got.State != db.StatePlaying || got.AppID != splatoon3AppID {
		t.Errorf("a player in Splatoon 3 reads %q %q, %v; want PLAYING it", got.State, got.AppID, err)
	}
	stored, err := h.srv.store.GetPresence(ctx, "heartbeat000004")
	if err != nil || stored.State != db.StateOnline || !stored.ReportedAt.Equal(old) {
		t.Errorf("an ONLINE report was rewritten: %q at %v, %v", stored.State, stored.ReportedAt, err)
	}
}
