package baas

import (
	"fmt"
	"net/url"
	"testing"
	"time"
)

// TestLoginIsThrottledPerClientAddress is the coverage for a real gap: before
// this, nothing capped how many passwords one caller could try against
// /1.0.0/login.
func TestLoginIsThrottledPerClientAddress(t *testing.T) {
	h := newHarness(t, func(cfg *Config) {
		cfg.LoginAttemptsPerWindow = 3
		cfg.LoginWindow = time.Minute
	})

	a := h.register()
	loginAttempt := func(password, xff string) int {
		resp := h.do(request{
			method: "POST", path: "/1.0.0/login",
			token: h.anonymousToken(),
			form: url.Values{
				"id":       {a.DeviceAccountID},
				"password": {password},
			},
			headers: map[string]string{"X-Forwarded-For": xff},
		})
		return resp.StatusCode
	}

	// Three wrong-password attempts from one address are each answered
	// normally (400 invalid_device_account, not throttled).
	for i := 0; i < 3; i++ {
		if code := loginAttempt("wrong", "203.0.113.9"); code != 400 {
			t.Fatalf("attempt %d: status = %d, want 400", i+1, code)
		}
	}

	// The fourth attempt from the SAME address is throttled, even with the
	// correct password: the limiter gates the attempt itself, not just
	// wrong-password ones -- otherwise it would not bound the guessing rate
	// at all.
	if code := loginAttempt(a.Password, "203.0.113.9"); code != 429 {
		t.Errorf("4th attempt from the same address: status = %d, want 429", code)
	}

	// A DIFFERENT address is unaffected -- the limit is per caller, not
	// global, or one noisy address would lock out every console behind a
	// shared operator NAT.
	if code := loginAttempt(a.Password, "203.0.113.99"); code != 200 {
		t.Errorf("a different address: status = %d, want 200 (unaffected by the first address's limit)", code)
	}

	// Advancing past the window lets the original address try again.
	h.advance(2 * time.Minute)
	if code := loginAttempt(a.Password, "203.0.113.9"); code != 200 {
		t.Errorf("after the window rolls forward: status = %d, want 200", code)
	}
}

// TestLoginRateLimitDisabledByDefaultInOtherTests documents the default: a
// zero LoginAttemptsPerWindow (what every other test's harness gets, since
// none of them opt in) never throttles, so this feature cannot make an
// unrelated test that logs in many times start failing.
func TestLoginRateLimitDisabledByDefaultInOtherTests(t *testing.T) {
	h := newHarness(t)
	a := h.register()

	for i := 0; i < 50; i++ {
		resp := h.do(request{
			method: "POST", path: "/1.0.0/login",
			token: h.anonymousToken(),
			form: url.Values{
				"id":       {a.DeviceAccountID},
				"password": {a.Password},
			},
		})
		if resp.StatusCode != 200 {
			t.Fatalf("attempt %d with the default (disabled) rate limit: status = %d, want 200",
				i+1, resp.StatusCode)
		}
	}
}

// A console signs in each of its user profiles at boot, and profiles never
// linked to GRID0+ are refused every time. Those refusals must not use up the
// linked profile's budget, or its game is throttled.
func TestUnlinkedProfilesOnAConsoleDoNotThrottleTheLinkedOne(t *testing.T) {
	h := newHarness(t, func(cfg *Config) {
		cfg.LoginAttemptsPerWindow = 3
		cfg.LoginWindow = time.Minute
	})
	a := h.register()
	attempt := func(id, password string) int {
		return h.do(request{
			method: "POST", path: "/1.0.0/login", token: h.anonymousToken(),
			form:    url.Values{"id": {id}, "password": {password}},
			headers: map[string]string{"X-Forwarded-For": "203.0.113.9"},
		}).StatusCode
	}
	for i := 0; i < 10; i++ {
		if code := attempt(fmt.Sprintf("%016x", 0xabc000+i), "whatever"); code != 400 {
			t.Fatalf("unlinked profile %d: status = %d, want 400", i, code)
		}
	}
	if code := attempt(a.DeviceAccountID, a.Password); code != 200 {
		t.Errorf("the linked profile after its console's unlinked ones: status = %d, want 200", code)
	}
}
