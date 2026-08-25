package presencebus

import (
	"context"
	"sync"
)

// bufferPerSubscriber is how many events a subscriber may fall behind by before
// events start being dropped for it. A subscriber is either a dashboard
// websocket or an NPLN presence stream; both consume events immediately, so a
// backlog of 64 means something is wedged and dropping is the right answer.
const bufferPerSubscriber = 64

// Local is an in-process Bus.
//
// It is correct when exactly one process publishes presence and every
// subscriber lives in that same process. That is the whole of phase 1, where
// only the BAAS server writes presence. As soon as a second process needs the
// events — the phase 2 dashboard running separately, or a second BAAS replica —
// use Redis instead, because a local bus silently delivers nothing across a
// process boundary.
type Local struct {
	mu     sync.Mutex
	subs   map[chan Event]struct{}
	closed bool

	// dropped counts events that could not be delivered because a subscriber
	// was too slow. Exposed for a log line at shutdown, so a wedged subscriber
	// leaves a trace instead of quietly losing updates.
	dropped uint64
}

// NewLocal returns an in-process bus.
func NewLocal() *Local {
	return &Local{subs: map[chan Event]struct{}{}}
}

// Publish delivers an event to every current subscriber.
func (l *Local) Publish(_ context.Context, e Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	for ch := range l.subs {
		select {
		case ch <- e:
		default:
			l.dropped++
		}
	}
	return nil
}

// Subscribe returns a channel of events, closed when ctx is cancelled.
func (l *Local) Subscribe(ctx context.Context) (<-chan Event, error) {
	ch := make(chan Event, bufferPerSubscriber)

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		close(ch)
		return ch, nil
	}
	l.subs[ch] = struct{}{}
	l.mu.Unlock()

	go func() {
		<-ctx.Done()
		l.mu.Lock()
		defer l.mu.Unlock()
		if _, ok := l.subs[ch]; ok {
			delete(l.subs, ch)
			close(ch)
		}
	}()

	return ch, nil
}

// Close closes every subscriber channel.
func (l *Local) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	for ch := range l.subs {
		delete(l.subs, ch)
		close(ch)
	}
	return nil
}

// Dropped returns how many events were dropped because a subscriber was slow.
func (l *Local) Dropped() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped
}

var _ Bus = (*Local)(nil)
