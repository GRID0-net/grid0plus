package natobs

import (
	"context"
	"os"
	"testing"
)

type store interface {
	Record(ctx context.Context, ip string, port int) error
	Lookup(ctx context.Context, ip string) (int, bool)
}

func roundTrip(t *testing.T, s store) {
	ctx := context.Background()
	if _, ok := s.Lookup(ctx, "203.0.113.250"); ok {
		t.Fatal("an unobserved IP has a port")
	}
	if err := s.Record(ctx, "203.0.113.250", 61000); err != nil {
		t.Fatal(err)
	}
	if p, ok := s.Lookup(ctx, "203.0.113.250"); !ok || p != 61000 {
		t.Fatalf("lookup = %d %v", p, ok)
	}
	if err := s.Record(ctx, "203.0.113.250", 62000); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Lookup(ctx, "203.0.113.250"); p != 62000 {
		t.Fatalf("a newer mapping did not replace the old: %d", p)
	}
}

func TestMemoryRoundTrip(t *testing.T) { roundTrip(t, NewMemory()) }

// TestRedisRoundTrip only runs when SWITCHNET_TEST_REDIS_URL is set.
func TestRedisRoundTrip(t *testing.T) {
	url := os.Getenv("SWITCHNET_TEST_REDIS_URL")
	if url == "" {
		t.Skip("SWITCHNET_TEST_REDIS_URL is not set")
	}
	r, err := NewRedis(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_ = r.client.Del(context.Background(), keyPrefix+"203.0.113.250").Err()
	roundTrip(t, r)
}
