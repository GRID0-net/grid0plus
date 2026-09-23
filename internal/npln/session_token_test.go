package npln

import (
	"strings"
	"testing"

	mmpb "github.com/n-popescu/switchnet/proto/matchmaking/v1"
)

// TestAMatchedSessionCarriesASessionTokenTheConsoleCanRead is the 2321-4992 fix
// asserted on the wire.
//
// The console never asks for this token: it reads it out of the ticket's
// matchedUserSessions and takes gsid, usid, uid and tid from the 'gamesync'
// object to build Gamesync/IssueToken and then KeepUserSession. So the token's
// shape is a client-facing contract, and this test decodes it by hand rather
// than through the verifier — a field present in the Go struct but mis-tagged
// would round-trip through this server's own code and still not be what the
// client reads.
func TestAMatchedSessionCarriesASessionTokenTheConsoleCanRead(t *testing.T) {
	h := newHarness(t)
	h.createUser("player-one", "device-one")
	h.createUser("player-two", "device-two")

	one := h.ctx(h.sessionFor(t, "player-one", "device-one"), "")
	two := h.ctx(h.sessionFor(t, "player-two", "device-two"), "")

	client := mmpb.NewMatchmakerClient(h.conn)
	if _, err := client.CreateMatchmakingTicket(one, ticketFor("turf-war")); err != nil {
		t.Fatalf("CreateMatchmakingTicket (one): %v", err)
	}
	second, err := client.CreateMatchmakingTicket(two, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket (two): %v", err)
	}
	if second.GetState() != mmpb.MatchmakingTicket_SUCCEEDED {
		t.Fatalf("the ticket that completes a match should succeed, got %v", second.GetState())
	}

	matched := second.GetMatchedUserSessions()
	if len(matched) != 2 {
		t.Fatalf("matched user sessions = %d, want one per player (2)", len(matched))
	}
	sessionID := lastSegment(second.GetGameSession().GetName())
	if sessionID == "" {
		t.Fatal("a succeeded ticket carries no game session to read a session id off")
	}

	for _, m := range matched {
		userID := lastSegment(m.GetUserDefinition().GetUser())
		token := m.GetMatchmakingIdToken()
		if token == "" {
			// The measured consequence rather than a style complaint: the
			// console opens its session channel and never sends a request.
			t.Fatalf("%s: no session token, so the console cannot open its gamesync session", userID)
		}

		segments := strings.Split(token, ".")
		if len(segments) != 3 {
			t.Fatalf("%s: session token has %d segments, want 3", userID, len(segments))
		}

		// A two-member header, which is what a real one carries: no 'jku' and
		// no 'typ'. A token that differs here is a difference the client has
		// never seen from this field.
		header := decodeJWTSegment(t, segments[0])
		if len(header) != 2 {
			t.Errorf("%s: session token header carries %v, want alg and kid only", userID, header)
		}
		if header["alg"] != "ES256" {
			t.Errorf("%s: session token alg = %v, want ES256", userID, header["alg"])
		}
		if kid, _ := header["kid"].(string); kid == "" {
			t.Errorf("%s: session token header carries no kid", userID)
		}

		// Five members, and the absence of the other two this package's claim
		// struct can carry is asserted here rather than assumed: 'aud' and
		// 'jti' are on every ES256 token SwitchNet mints, and the capture shows
		// neither on this one.
		payload := decodeJWTSegment(t, segments[1])
		if len(payload) != 5 {
			t.Errorf("%s: session token payload carries %v, want exp, iat, iss, sub and gamesync only", userID, payload)
		}
		if payload["iss"] != "gss" {
			t.Errorf("%s: session token iss = %v, want \"gss\"", userID, payload["iss"])
		}
		if payload["sub"] != userID {
			t.Errorf("%s: session token sub = %v", userID, payload["sub"])
		}
		iat, _ := payload["iat"].(float64)
		exp, _ := payload["exp"].(float64)
		if exp-iat != 3600 {
			t.Errorf("%s: session token lifetime = %v s, want 3600 (exp - iat in the capture)", userID, exp-iat)
		}

		gs, ok := payload["gamesync"].(map[string]any)
		if !ok {
			t.Fatalf("%s: session token carries no gamesync object, which is the part the client reads: %v",
				userID, payload["gamesync"])
		}
		for _, field := range []string{"attr", "gsid", "ltcy", "team", "tid", "typ", "uid", "usid"} {
			if _, present := gs[field]; !present {
				t.Errorf("%s: gamesync object has no %q: %v", userID, field, gs)
			}
		}
		if gs["gsid"] != sessionID {
			t.Errorf("%s: gamesync.gsid = %v, want the session id %q", userID, gs["gsid"], sessionID)
		}
		if gs["uid"] != userID {
			t.Errorf("%s: gamesync.uid = %v", userID, gs["uid"])
		}
		if gs["usid"] != userID {
			t.Errorf("%s: gamesync.usid = %v, want the user session name's last segment (%s)",
				userID, gs["usid"], userID)
		}
		if gs["tid"] != testTenant {
			t.Errorf("%s: gamesync.tid = %v, want the tenant %q", userID, gs["tid"], testTenant)
		}
		if gs["typ"] != float64(1) {
			t.Errorf("%s: gamesync.typ = %v, want 1", userID, gs["typ"])
		}

		// The token the client reads is also the one it presents back to
		// gamesync, so it has to be a token this server accepts.
		resolved, err := h.srv.VerifyMatchmakingIDToken(token)
		if err != nil {
			t.Fatalf("%s: the session token does not verify: %v", userID, err)
		}
		if resolved != userID {
			t.Errorf("%s: session token resolved to %q", userID, resolved)
		}
	}
}

// TestAnAccessTokenIsNotASessionToken keeps the seam between the two credentials
// closed now that the session token's shape is the console's to read.
//
// Both are ES256 and signed by the same key, so the issuer and the gamesync
// block are what separate them: an access token acts on the account API, and
// letting it open a gamesync session would let that credential stand in for a
// place in a match.
func TestAnAccessTokenIsNotASessionToken(t *testing.T) {
	h := newHarness(t)
	h.createUser("player-one", "device-one")

	access := h.sessionFor(t, "player-one", "device-one")
	if access == "" {
		t.Fatal("the harness issued no access token to test with")
	}
	if _, err := h.srv.VerifyMatchmakingIDToken(access); err == nil {
		t.Fatal("an NPLN access token was accepted as a gamesync session token")
	}
}
