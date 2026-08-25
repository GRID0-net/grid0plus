package presencebus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

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

// Close closes the Redis client.
func (r *Redis) Close() error { return r.client.Close() }

var _ Bus = (*Redis)(nil)
