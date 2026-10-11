package npln

import (
	"testing"

	authpb "github.com/n-popescu/grid0plus/proto/auth/v1"
)

// TestExternalIdsAnswerOnlyAccountsThatExist: a player's NSA id is what a
// friend request is sent to, so the lookup answers real accounts both ways
// and nothing for an id nobody holds.
func TestExternalIdsAnswerOnlyAccountsThatExist(t *testing.T) {
	h := newHarness(t)
	h.createUser("asker00000000001", "device-asker-1")
	h.createUser("other00000000001", "device-other-1")
	asker := h.ctx(h.sessionFor(t, "asker00000000001", "device-asker-1"), "")
	client := authpb.NewUserServiceClient(h.conn)

	resp, err := client.QueryUserExternalIds(asker, &authpb.QueryUserExternalIdsRequest{
		Condition: &authpb.QueryUserExternalIdsRequest_ExternalIdCondition_{
			ExternalIdCondition: &authpb.QueryUserExternalIdsRequest_ExternalIdCondition{
				Users: []string{
					"tenants/" + testTenant + "/users/other00000000001",
					"tenants/" + testTenant + "/users/nobody0000000001",
				},
				ExternalIdTypes: []authpb.ExternalIdType{authpb.ExternalIdType_NSA_ID},
			},
		},
	})
	if err != nil {
		t.Fatalf("QueryUserExternalIds: %v", err)
	}
	got := resp.GetUserExternalIds()
	if len(got) != 1 || got[0].GetExternalId().GetId() != "other00000000001" ||
		got[0].GetExternalId().GetType() != authpb.ExternalIdType_NSA_ID {
		t.Fatalf("answered %v, want only other's NSA id", got)
	}

	back, err := client.QueryUserExternalIds(asker, &authpb.QueryUserExternalIdsRequest{
		Condition: &authpb.QueryUserExternalIdsRequest_UserCondition_{
			UserCondition: &authpb.QueryUserExternalIdsRequest_UserCondition{
				ExternalIds: []*authpb.ExternalId{{Type: authpb.ExternalIdType_NSA_ID, Id: "other00000000001"}},
			},
		},
	})
	if err != nil || len(back.GetUserExternalIds()) != 1 ||
		back.GetUserExternalIds()[0].GetUser() != "tenants/"+testTenant+"/users/other00000000001" {
		t.Fatalf("reverse lookup = %v, %v", back.GetUserExternalIds(), err)
	}
}
