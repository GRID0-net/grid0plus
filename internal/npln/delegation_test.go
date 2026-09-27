package npln

import (
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
