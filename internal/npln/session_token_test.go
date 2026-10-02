package npln

import (
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	mmpb "github.com/n-popescu/grid0plus/proto/matchmaking/v1"
	"google.golang.org/protobuf/types/known/durationpb"
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
	request := ticketFor("turf-war")
	request.MatchmakingTicket.UserDefinitions = []*mmpb.UserDefinition{{
		Attributes: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"match_hash": {ValueType: &commonpb.Value_StringValue{StringValue: "sample-match"}},
			"mmr":        {ValueType: &commonpb.Value_IntegerValue{IntegerValue: 42}},
		}},
		LatencyData: &mmpb.LatencyData{Latencies: map[string]*durationpb.Duration{
			"region-a": durationpb.New(25_000_000),
		}},
	}}
	second, err := client.CreateMatchmakingTicket(two, request)
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket (two): %v", err)
	}
	second = trackResult(t, client, two, second)

	matched := second.GetMatchedUserSessions()
	// Only the recipient's own entry: the players find each other on the
	// session host, never in the ticket (every Nintendo capture).
	if len(matched) != 1 || lastSegment(matched[0].GetUserDefinition().GetUser()) != "player-two" {
		t.Fatalf("matched user sessions = %v, want player-two's alone", matched)
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
		// 'jti' are on every ES256 token GRID0+ mints, and the capture shows
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
		for _, field := range []string{"attr", "gsid", "ltcy", "mcn", "pid", "relay_host", "relay_port",
			"team", "tid", "typ", "uid", "usid"} {
			if _, present := gs[field]; !present {
				t.Errorf("%s: gamesync object has no %q: %v", userID, field, gs)
			}
		}
		// Nextendo's matched-session tokens carry no friend_room at all; only
		// room tokens do, set to true.
		if _, present := gs["friend_room"]; present {
			t.Errorf("%s: a matched session's token claims friend_room: %v", userID, gs)
		}
		if gs["mcn"] != "turf-war" {
			t.Errorf("%s: gamesync.mcn = %v, want the matchmaking config's last segment", userID, gs["mcn"])
		}
		if pid, _ := gs["pid"].(float64); pid <= 0 || pid >= 1<<31 {
			t.Errorf("%s: gamesync.pid = %v, want a positive 31-bit number", userID, gs["pid"])
		}
		if gs["gsid"] != sessionID {
			t.Errorf("%s: gamesync.gsid = %v, want the session id %q", userID, gs["gsid"], sessionID)
		}
		if gs["uid"] != userID {
			t.Errorf("%s: gamesync.uid = %v", userID, gs["uid"])
		}
		// usid is the user session's own id -- the last segment of the name
		// the ticket hands out -- and, as in the capture, a UUID distinct
		// from uid.
		usid, _ := gs["usid"].(string)
		if want := lastSegment(m.GetUserSession()); usid != want {
			t.Errorf("%s: gamesync.usid = %q, want the user session name's last segment %q",
				userID, usid, want)
		}
		if _, err := uuid.Parse(usid); err != nil || usid == userID {
			t.Errorf("%s: gamesync.usid = %q, want a UUID distinct from uid", userID, usid)
		}
		if gs["tid"] != testTenant {
			t.Errorf("%s: gamesync.tid = %v, want the tenant %q", userID, gs["tid"], testTenant)
		}
		if gs["typ"] != float64(1) {
			t.Errorf("%s: gamesync.typ = %v, want 1", userID, gs["typ"])
		}
		var attrs map[string]map[string]any
		if err := json.Unmarshal([]byte(gs["attr"].(string)), &attrs); err != nil {
			t.Fatalf("gamesync.attr is not typed JSON: %v", err)
		}
		if attrs["match_hash"]["type"] != "string" || attrs["match_hash"]["value"] != "sample-match" ||
			attrs["mmr"]["type"] != "integer" || attrs["mmr"]["value"] != float64(42) {
			t.Errorf("gamesync.attr lost the console's submitted values: %v", attrs)
		}
		var latencies struct {
			Latencies map[string]struct {
				Nanos int64 `json:"nanos"`
			} `json:"latencies"`
		}
		if err := json.Unmarshal([]byte(gs["ltcy"].(string)), &latencies); err != nil {
			t.Fatalf("gamesync.ltcy is not JSON: %v", err)
		}
		if latencies.Latencies["region-a"].Nanos != 25_000_000 {
			t.Errorf("gamesync.ltcy lost the console's region measurement: %v", latencies.Latencies)
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

func TestGamesyncProbeTokenBindsTheRoomAndUserSession(t *testing.T) {
	h := newHarness(t)
	userSession := "tenants/" + testTenant + "/gameSessions/room-1/userSessions/player-1"
	token, err := h.srv.IssueGamesyncProbeToken("player-1", userSession, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("probe token has %d parts, want a signed JWT", len(parts))
	}
	payload := decodeJWTSegment(t, parts[1])
	if payload["sub"] != "player-1" || payload["iss"] != "default iss" {
		t.Errorf("probe token identity = %v", payload)
	}
	gss, ok := payload["gss"].(map[string]any)
	if !ok || gss["game_session"] != "tenants/"+testTenant+"/gameSessions/room-1" || gss["user_session"] != userSession {
		t.Errorf("probe token room binding = %v", payload["gss"])
	}
	if _, err := h.srv.IssueGamesyncProbeToken("player-1", "userSessions/short-name", time.Now().Add(time.Hour)); err != nil {
		t.Errorf("rejected a short user-session name the baseline accepts: %v", err)
	}
}
