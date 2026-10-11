package npln

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	authpb "github.com/n-popescu/grid0plus/proto/auth/v1"
	mmpb "github.com/n-popescu/grid0plus/proto/matchmaking/v1"
)

// TestASquadMemberCanDelegateToTheirLeader is the call a squad cannot queue
// without: it used to answer Unimplemented.
func TestASquadMemberCanDelegateToTheirLeader(t *testing.T) {
	h := newHarness(t)
	h.createUser("member0000000001", "device-member-1")
	h.createUser("leader0000000001", "device-leader-1")
	member := h.ctx(h.sessionFor(t, "member0000000001", "device-member-1"), "")

	resp, err := mmpb.NewGameSessionServiceClient(h.conn).IssueUserDelegationToken(member, &mmpb.IssueUserDelegationTokenRequest{
		DelegatorUser:     "tenants/current/users/current",
		MandataryUser:     "tenants/" + testTenant + "/users/leader0000000001",
		DelegationActions: []mmpb.DelegationAction{mmpb.DelegationAction_ANY_PARTICIPATION},
	})
	if err != nil {
		t.Fatalf("IssueUserDelegationToken: %v", err)
	}
	d := resp.GetUserDelegationDetail()
	if d.GetDelegatorUser() != "tenants/"+testTenant+"/users/member0000000001" {
		t.Errorf("delegator = %q, want the caller, resolved", d.GetDelegatorUser())
	}
	if d.GetMandataryUser() != "tenants/"+testTenant+"/users/leader0000000001" {
		t.Errorf("mandatary = %q", d.GetMandataryUser())
	}
	if d.GetUserDelegationToken() == "" || d.GetTtl().AsDuration() <= 0 {
		t.Errorf("detail carries no token or TTL: %v", d)
	}
	if len(d.GetDelegationActions()) != 1 {
		t.Errorf("delegation actions not echoed: %v", d.GetDelegationActions())
	}

	// The leader holds this token; it names the member. As a bearer it would
	// let the leader act as the member, so it must be refused as one.
	if _, err := authpb.NewAuthClient(h.conn).ValidateToken(h.ctx(d.GetUserDelegationToken(), ""), &emptypb.Empty{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("a delegation token as a bearer: %v, want Unauthenticated", err)
	}
}

// TestADelegationCannotBeIssuedInSomebodyElsesName: the delegator is the
// caller, never the user the request names.
func TestADelegationCannotBeIssuedInSomebodyElsesName(t *testing.T) {
	h := newHarness(t)
	h.createUser("member0000000002", "device-member-2")
	h.createUser("victim0000000002", "device-victim-2")
	member := h.ctx(h.sessionFor(t, "member0000000002", "device-member-2"), "")
	client := mmpb.NewGameSessionServiceClient(h.conn)

	_, err := client.IssueUserDelegationToken(member, &mmpb.IssueUserDelegationTokenRequest{
		DelegatorUser: "tenants/" + testTenant + "/users/victim0000000002",
		MandataryUser: "tenants/" + testTenant + "/users/member0000000002",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("delegating as another user: %v, want PermissionDenied", err)
	}

	_, err = client.IssueUserDelegationToken(member, &mmpb.IssueUserDelegationTokenRequest{
		MandataryUser: "tenants/" + testTenant + "/users/nobody0000000000",
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("delegating to an account that does not exist: %v, want NotFound", err)
	}
}

// TestASquadQueuesOnItsLeadersTicketAndEveryMemberFollowsIt is the Salmon Run
// room flow on hardware (2026-10-03): the guest delegates to the host, the
// host's one ticket carries that token, and the guest follows the host's
// ticket. Refusing the guest's Track was error 2321-1536.
func TestASquadQueuesOnItsLeadersTicketAndEveryMemberFollowsIt(t *testing.T) {
	h := newHarness(t)
	h.createUser("member0000000003", "device-member-3")
	h.createUser("leader0000000003", "device-leader-3")
	h.createUser("outsider00000003", "device-outsider-3")
	member := h.ctx(h.sessionFor(t, "member0000000003", "device-member-3"), "")
	leader := h.ctx(h.sessionFor(t, "leader0000000003", "device-leader-3"), "")
	outsider := h.ctx(h.sessionFor(t, "outsider00000003", "device-outsider-3"), "")

	d, err := mmpb.NewGameSessionServiceClient(h.conn).IssueUserDelegationToken(member, &mmpb.IssueUserDelegationTokenRequest{
		DelegatorUser: "tenants/current/users/current",
		MandataryUser: "tenants/" + testTenant + "/users/leader0000000003",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := ticketFor("turf-war")
	req.UserDelegationTokens = []string{d.GetUserDelegationDetail().GetUserDelegationToken()}
	client := mmpb.NewMatchmakerClient(h.conn)
	ticket, err := client.CreateMatchmakingTicket(leader, req)
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket: %v", err)
	}

	// Two per match by default: the squad alone fills it.
	for name, ctx := range map[string]context.Context{"leader": leader, "member": member} {
		stream, err := client.TrackMatchmakingTicket(ctx, &mmpb.TrackMatchmakingTicketRequest{Name: ticket.GetName()})
		if err != nil {
			t.Fatalf("%s Track: %v", name, err)
		}
		got := awaitSucceeded(t, stream)
		if n := len(got.GetMatchedUserSessions()); n != 2 {
			t.Errorf("%s: %d matched user sessions, want one per squad member", name, n)
		}
	}

	stream, err := client.TrackMatchmakingTicket(outsider, &mmpb.TrackMatchmakingTicketRequest{Name: ticket.GetName()})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.NotFound {
		t.Errorf("an outsider following the squad's ticket: %v, want NotFound", err)
	}
}

// TestALeaderGetsIdTokensOnlyForTeammatesWhoDelegated: the leader may mint a
// teammate's session token only with that teammate's delegation, and only
// for someone already in the session -- otherwise one console could open a
// gamesync session as another, the bug this codebase is shaped around.
func TestALeaderGetsIdTokensOnlyForTeammatesWhoDelegated(t *testing.T) {
	h := newHarness(t)
	h.createUser("host0000000000009", "device-host-9")
	h.createUser("friend00000000009", "device-friend-9")
	h.createUser("other000000000009", "device-other-9")
	host := h.ctx(h.sessionFor(t, "host0000000000009", "device-host-9"), "")
	friend := h.ctx(h.sessionFor(t, "friend00000000009", "device-friend-9"), "")
	other := h.ctx(h.sessionFor(t, "other000000000009", "device-other-9"), "")
	client := mmpb.NewGameSessionServiceClient(h.conn)

	room := openRoom(t, h, host, "coop_private_config", nil, "").GetGameSession()
	for _, c := range []context.Context{friend, other} {
		if _, err := client.JoinGameSession(c, &mmpb.JoinGameSessionRequest{Name: room.GetName()}); err != nil {
			t.Fatalf("JoinGameSession: %v", err)
		}
	}
	d, err := client.IssueUserDelegationToken(friend, &mmpb.IssueUserDelegationTokenRequest{
		DelegatorUser: "tenants/current/users/current",
		MandataryUser: "tenants/" + testTenant + "/users/host0000000000009",
	})
	if err != nil {
		t.Fatal(err)
	}
	token := d.GetUserDelegationDetail().GetUserDelegationToken()

	resp, err := client.IssueMatchmakingIdToken(host, &mmpb.IssueMatchmakingIdTokenRequest{
		GameSession:          room.GetName(),
		Users:                []string{"tenants/current/users/current", "tenants/" + testTenant + "/users/friend00000000009"},
		UserDelegationTokens: []string{token},
	})
	if err != nil {
		t.Fatalf("IssueMatchmakingIdToken: %v", err)
	}
	subs := map[any]bool{}
	for _, m := range resp.GetMatchedUserSessions() {
		subs[decodeJWTSegment(t, strings.Split(m.GetMatchmakingIdToken(), ".")[1])["sub"]] = true
	}
	if len(subs) != 2 || !subs["host0000000000009"] || !subs["friend00000000009"] {
		t.Errorf("tokens issued for %v, want the host and the friend", subs)
	}

	_, err = client.IssueMatchmakingIdToken(host, &mmpb.IssueMatchmakingIdTokenRequest{
		GameSession: room.GetName(),
		Users:       []string{"tenants/" + testTenant + "/users/other000000000009"},
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("a token for a player who never delegated: %v, want PermissionDenied", err)
	}
}
