package baas

import (
	"sync"
	"time"
)

// loginLimiter is a sliding-window rate limiter over login attempts, keyed by
// whatever the caller passes: client address plus device account for
// /1.0.0/login (see completeLogin), client address for a username login.
//
// completeLogin's password check is the one place a wrong guess costs the
// caller almost nothing — a device account's password is generated,
// high-entropy and never user-chosen, so unbounded guessing was a smaller
// practical risk here than for a password a person picked, but there was
// previously no limit of any kind on this endpoint.
//
// A rejected attempt does not count against the window itself — the failure
// mode this guards against is many failed passwords, not many successful
// logins, and counting the rejection too would let an attacker who is
// already blocked keep the window pinned open.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time

	max    int
	window time.Duration

	// sweptAt is when attempts was last swept of every address with nothing
	// left in the window. Bounds the map's size for many distinct addresses
	// that each stop trying, without a background goroutine to manage.
	sweptAt time.Time
}

// sweepInterval is how often Allow opportunistically drops address entries
// with nothing left in the window, when the limiter is enabled.
const sweepInterval = time.Minute

// newLoginLimiter builds a limiter allowing max attempts per window, per
// address. max <= 0 or window <= 0 disables the limit: every call to Allow
// returns true without recording anything.
func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{
		attempts: map[string][]time.Time{},
		max:      max,
		window:   window,
	}
}

// Allow reports whether another attempt from key is permitted right now, and
// records it if so. Call it once per attempt, at the point the attempt is
// actually made (after the request is otherwise well-formed, so a malformed
// request cannot burn someone else's budget).
func (l *loginLimiter) Allow(key string, now time.Time) bool {
	if l.max <= 0 || l.window <= 0 {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.sweptAt) >= sweepInterval {
		l.sweepLocked(now)
	}

	cutoff := now.Add(-l.window)
	kept := l.attempts[key][:0]
	for _, t := range l.attempts[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}

	if len(kept) >= l.max {
		l.attempts[key] = kept
		return false
	}

	l.attempts[key] = append(kept, now)
	return true
}

// sweepLocked drops every address with no attempts left inside the window.
// Caller holds l.mu.
func (l *loginLimiter) sweepLocked(now time.Time) {
	cutoff := now.Add(-l.window)
	for key, times := range l.attempts {
		stillLive := false
		for _, t := range times {
			if t.After(cutoff) {
				stillLive = true
				break
			}
		}
		if !stillLive {
			delete(l.attempts, key)
		}
	}
	l.sweptAt = now
}
