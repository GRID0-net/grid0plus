package presencebus

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func testEvent(userID, state string) Event {
	return Event{
		UserID: userID, State: state,
		AppID: "0100c2500fc20000", PresenceGroupID: "0100c2500fc20000",
		AppField: `{"mode":"regular"}`,
		At:       time.Now().UTC().Truncate(time.Second),
		Source:   "baas",
	}
}

func TestLocalDeliversToEverySubscriber(t *testing.T) {
	bus := NewLocal()
	defer func() { _ = bus.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	first, err := bus.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := bus.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	want := testEvent("aaaaaaaaaaaaaaaa", "PLAYING")
	if err := bus.Publish(ctx, want); err != nil {
		t.Fatal(err)
	}

	// Every subscriber gets the event: the dashboard and the NPLN presence
	// service are separate consumers of the same change.
	for i, ch := range []<-chan Event{first, second} {
		select {
		case got := <-ch:
			if got.UserID != want.UserID || got.State != want.State {
				t.Errorf("subscriber %d got %+v, want %+v", i, got, want)
			}
			if got.AppField != want.AppField {
				t.Errorf("subscriber %d: appField = %q, want it passed through", i, got.AppField)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %d received nothing", i)
		}
	}
}

func TestLocalClosesTheChannelWhenTheContextIsCancelled(t *testing.T) {
	bus := NewLocal()
	defer func() { _ = bus.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	ch, err := bus.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cancel()

	// A subscriber that goes away has to be cleaned up, or a long-running server
	// leaks a channel per dashboard tab that was ever opened.
	select {
	case _, open := <-ch:
		if open {
			t.Error("the channel delivered an event after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the channel was not closed after the context was cancelled")
	}
}

// TestLocalDropsRatherThanBlockingOnASlowSubscriber is the property that keeps a
// wedged consumer from stalling a console's request.
//
// Publish is called on the request path of a presence report. If it blocked
// waiting for a subscriber that had stopped reading, the console would hang.
func TestLocalDropsRatherThanBlockingOnASlowSubscriber(t *testing.T) {
	bus := NewLocal()
	defer func() { _ = bus.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Subscribe and never read.
	if _, err := bus.Subscribe(ctx); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range bufferPerSubscriber * 3 {
			_ = bus.Publish(ctx, testEvent("aaaaaaaaaaaaaaaa", "ONLINE"))
			_ = i
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a subscriber that stopped reading")
	}

	if bus.Dropped() == 0 {
		t.Error("nothing was counted as dropped, so the overflow went unnoticed")
	}
}

func TestLocalPublishAfterCloseIsNotAnError(t *testing.T) {
	bus := NewLocal()
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
	// Shutdown order is not guaranteed: the HTTP server may still be draining a
	// request when the bus is closed. Publishing then must not fail that request.
	if err := bus.Publish(t.Context(), testEvent("aaaaaaaaaaaaaaaa", "OFFLINE")); err != nil {
		t.Errorf("publish after close: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

// ---- Redis ---------------------------------------------------------------

// TestRedisRoundTrip only runs when SWITCHNET_TEST_REDIS_URL is set, so
// `go test ./...` works without Redis. CI sets it.
func TestRedisRoundTrip(t *testing.T) {
	url := os.Getenv("SWITCHNET_TEST_REDIS_URL")
	if url == "" {
		t.Skip("SWITCHNET_TEST_REDIS_URL is not set")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// A channel name per test run, so a leftover subscriber from another run
	// cannot make this pass or fail spuriously.
	channel := "switchnet:test:presence:" + time.Now().Format("150405.000000000")

	bus, err := NewRedis(ctx, url, channel, discardLogger())
	if err != nil {
		t.Fatalf("connect to redis: %v", err)
	}
	defer func() { _ = bus.Close() }()

	ch, err := bus.Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	want := testEvent("b4922963e6b8deb2", "PLAYING")
	if err := bus.Publish(ctx, want); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got := <-ch:
		if got.UserID != want.UserID || got.State != want.State || got.AppID != want.AppID {
			t.Errorf("got %+v, want %+v", got, want)
		}
		// The timestamp has to survive the JSON round trip: the dashboard orders
		// on it.
		if !got.At.Equal(want.At) {
			t.Errorf("At = %v, want %v", got.At, want.At)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the event did not arrive")
	}
}

// TestRedisIgnoresAMalformedMessage proves a stray publisher on our channel does
// not tear down the subscription.
func TestRedisIgnoresAMalformedMessage(t *testing.T) {
	url := os.Getenv("SWITCHNET_TEST_REDIS_URL")
	if url == "" {
		t.Skip("SWITCHNET_TEST_REDIS_URL is not set")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	channel := "switchnet:test:malformed:" + time.Now().Format("150405.000000000")
	bus, err := NewRedis(ctx, url, channel, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bus.Close() }()

	ch, err := bus.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	raw := redis.NewClient(opt)
	defer func() { _ = raw.Close() }()

	if err := raw.Publish(ctx, channel, "this is not JSON").Err(); err != nil {
		t.Fatal(err)
	}
	// A good event after the bad one must still arrive.
	want := testEvent("aaaaaaaaaaaaaaaa", "ONLINE")
	if err := bus.Publish(ctx, want); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-ch:
		if got.UserID != want.UserID {
			t.Errorf("got %+v after a malformed message, want %+v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a malformed message killed the subscription")
	}
}

// discardLogger keeps test output clean.
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// TestAStatsSnapshotReplacesTheLastOneAndGoesStale.
//
// Stats is a GAUGE, not an event, and both halves of that matter. A freshly
// loaded dashboard needs the current value rather than the next change, so it is
// readable. And an expired snapshot must read as ABSENT rather than as zero: on
// a tile, "nobody is queuing" and "the game server is not running" look
// identical, and only one of them is worth acting on.
func TestAStatsSnapshotReplacesTheLastOneAndGoesStale(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	bus := NewLocal()
	bus.now = func() time.Time { return now }
	defer bus.Close()

	ctx := context.Background()

	// With nothing published there is no data -- which is not the same as an
	// empty queue.
	if _, ok, err := bus.Stats(ctx); err != nil || ok {
		t.Errorf("Stats before anything was published = ok %v, err %v", ok, err)
	}

	if err := bus.PublishStats(ctx, Stats{Matchmaking: map[string]int{"turf_war": 3}}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, ok, err := bus.Stats(ctx)
	if err != nil || !ok {
		t.Fatalf("Stats = ok %v, err %v", ok, err)
	}
	if got.Matchmaking["turf_war"] != 3 {
		t.Errorf("matchmaking = %v", got.Matchmaking)
	}
	// The timestamp is filled in when absent, because the reader's staleness
	// check depends on it and a zero time would read as permanently stale.
	if got.At != now {
		t.Errorf("At = %s, want the publish time", got.At)
	}

	// Replaced, not accumulated: two players leaving must be visible.
	if err := bus.PublishStats(ctx, Stats{Matchmaking: map[string]int{"turf_war": 1}}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, _, _ = bus.Stats(ctx)
	if got.Matchmaking["turf_war"] != 1 {
		t.Errorf("matchmaking = %v, want the newer snapshot", got.Matchmaking)
	}

	// And past the TTL it is gone.
	now = now.Add(2 * StatsTTL)
	if _, ok, _ := bus.Stats(ctx); ok {
		t.Error("a stale snapshot is still reported as current")
	}
}

// TestAnEmptyQueueIsNotTheSameAsNoData is the distinction the dashboard tile is
// built on.
func TestAnEmptyQueueIsNotTheSameAsNoData(t *testing.T) {
	bus := NewLocal()
	defer bus.Close()
	ctx := context.Background()

	if err := bus.PublishStats(ctx, Stats{Matchmaking: map[string]int{}}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, ok, err := bus.Stats(ctx)
	if err != nil || !ok {
		t.Fatalf("an empty-but-present snapshot reads as absent: ok %v, err %v", ok, err)
	}
	if len(got.Matchmaking) != 0 {
		t.Errorf("matchmaking = %v, want empty", got.Matchmaking)
	}
}
