// Package natobs carries what the NAT-check responder observes -- the
// external UDP port each console's router mapped -- to the NEX game server,
// which is a different process.
//
// Why the game server needs it. A console registers the stations peers
// should reach it at, but behind a router it only knows its LAN side: the
// public station the game server builds from the connection's address has
// the right IP and the console's LOCAL port. The NAT-check responder
// (cmd/natcheck) is the one component that sees the port the router actually
// mapped for the console's Pia socket, because Pia runs its NAT check from
// that socket. GRID0+'s services already share Redis, so the observation goes
// there.
//
// Keyed by public IP, so an observation is only usable while one console at
// a time sits behind that IP -- see rendezvous's bridge.
package natobs

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// TTL is how long an observation is believed. A NAT mapping for an idle
// socket expires in minutes on most routers; a console that is playing
// re-checks long before this.
const TTL = 10 * time.Minute

const keyPrefix = "switchnet:natobs:"

// Redis stores observations in Redis.
type Redis struct {
	client *redis.Client

	mu   sync.Mutex
	last map[string]recent
}

type recent struct {
	port int
	at   time.Time
}

// NewRedis connects to url.
func NewRedis(ctx context.Context, url string) (*Redis, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	c := redis.NewClient(opt)
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return &Redis{client: c, last: map[string]recent{}}, nil
}

// Close closes the connection.
func (r *Redis) Close() error { return r.client.Close() }

// refresh is how often an unchanged observation is re-written: a console
// sends several NAT-check messages a session, and each need not be a write.
const refresh = TTL / 3

// Record notes that ip's router mapped port.
func (r *Redis) Record(ctx context.Context, ip string, port int) error {
	now := time.Now()
	r.mu.Lock()
	if prev, ok := r.last[ip]; ok && prev.port == port && now.Sub(prev.at) < refresh {
		r.mu.Unlock()
		return nil
	}
	r.last[ip] = recent{port: port, at: now}
	if len(r.last) > 65536 {
		r.last = map[string]recent{ip: {port: port, at: now}}
	}
	r.mu.Unlock()
	return r.client.Set(ctx, keyPrefix+ip, strconv.Itoa(port), TTL).Err()
}

// Lookup returns the last port observed for ip.
func (r *Redis) Lookup(ctx context.Context, ip string) (int, bool) {
	v, err := r.client.Get(ctx, keyPrefix+ip).Result()
	if err != nil {
		return 0, false
	}
	p, err := strconv.Atoi(v)
	if err != nil || p <= 0 || p > 65535 {
		return 0, false
	}
	return p, true
}

// Memory is an in-process store, for tests and for a single-process setup.
type Memory struct {
	mu sync.Mutex
	m  map[string]int
}

// NewMemory returns an empty store.
func NewMemory() *Memory { return &Memory{m: map[string]int{}} }

// Record notes an observation.
func (m *Memory) Record(_ context.Context, ip string, port int) error {
	m.mu.Lock()
	m.m[ip] = port
	m.mu.Unlock()
	return nil
}

// Lookup returns an observation.
func (m *Memory) Lookup(_ context.Context, ip string) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.m[ip]
	return p, ok
}
