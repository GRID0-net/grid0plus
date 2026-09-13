package presencebus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis is a Bus backed by Redis pub/sub.
//
// Use it as soon as more than one process is involved: the phase 2 dashboard,
// the NPLN server, or a second BAAS replica. Redis pub/sub is fire-and-forget,
// which suits presence exactly — an event that arrives late is worthless, and a
// subscriber that was not connected has nothing to catch up on because it will
// read current state from Postgres when it starts.
type Redis struct {
	client  *redis.Client
	channel string
	log     *slog.Logger
}

// NewRedis connects to Redis and returns a bus publishing on channel.
func NewRedis(ctx context.Context, url, channel string, log *slog.Logger) (*Redis, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	client := redis.NewClient(opt)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return &Redis{client: client, channel: channel, log: log}, nil
}

// Publish sends an event.
func (r *Redis) Publish(ctx context.Context, e Event) error {
	buf, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return r.client.Publish(ctx, r.channel, buf).Err()
}

// Subscribe returns a channel of events, closed when ctx is cancelled.
func (r *Redis) Subscribe(ctx context.Context) (<-chan Event, error) {
	sub := r.client.Subscribe(ctx, r.channel)
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, fmt.Errorf("subscribe to %s: %w", r.channel, err)
	}

	out := make(chan Event, bufferPerSubscriber)
	go func() {
		defer close(out)
		defer func() { _ = sub.Close() }()
		for msg := range sub.Channel() {
			var e Event
			if err := json.Unmarshal([]byte(msg.Payload), &e); err != nil {
				// A malformed message means something else is publishing on our
				// channel. Log it and carry on rather than tearing down the
				// subscription.
				r.log.Warn("presencebus: discarding malformed event", "error", err)
				continue
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// statsKey is where the snapshot lives.
//
// Derived from the channel name so that two SwitchNet deployments sharing one
// Redis do not read each other's gauge -- which would be a very confusing way to
// find out the channels were shared.
func (r *Redis) statsKey() string { return r.channel + ":stats" }

// PublishStats replaces the current snapshot.
//
// SET with an expiry rather than a publish: this is a gauge, and a dashboard
// that has just been opened needs the current value rather than the next
// change. The expiry is what makes a game server that stopped read as absent
// instead of as "nobody is queuing".
func (r *Redis) PublishStats(ctx context.Context, s Stats) error {
	if s.At.IsZero() {
		s.At = time.Now().UTC()
	}
	buf, err := json.Marshal(s)
	if err != nil {
		return err
	}
	// Twice the TTL, so the reader's own staleness check is the one that
	// decides -- rather than the key vanishing a moment before the check and
	// the two disagreeing about why there is no data.
	return r.client.Set(ctx, r.statsKey(), buf, 2*StatsTTL).Err()
}

// Stats returns the current snapshot, or false when there is none or it is stale.
func (r *Redis) Stats(ctx context.Context) (Stats, bool, error) {
	buf, err := r.client.Get(ctx, r.statsKey()).Bytes()
	if errors.Is(err, redis.Nil) {
		return Stats{}, false, nil
	}
	if err != nil {
		return Stats{}, false, err
	}
	var s Stats
	if err := json.Unmarshal(buf, &s); err != nil {
		// Something else is writing this key. Reported as absent rather than as
		// an error, for the same reason a malformed event is discarded: a
		// cosmetic tile must not take the page down.
		r.log.Warn("presencebus: discarding malformed stats", "error", err)
		return Stats{}, false, nil
	}
	if time.Since(s.At) > StatsTTL {
		return Stats{}, false, nil
	}
	return s, true, nil
}

// Close closes the Redis client.
func (r *Redis) Close() error { return r.client.Close() }

var _ Bus = (*Redis)(nil)
