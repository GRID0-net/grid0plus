package npln

import (
	"testing"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// TestInitialVsMarksEveryModesRatingInitialised: without the
// mmr_initialized_<mode> flags the console re-sent initial_vs forever from
// the hall (2026-09-25 hardware).
func TestInitialVsMarksEveryModesRatingInitialised(t *testing.T) {
	const uid = "gr00000000000001"
	h := newHarness(t)
	h.createUser(uid, "device-gr-1")
	ctx := h.ctx(h.sessionFor(t, uid, "device-gr-1"), "")
	resp, err := toyohrpb.NewGameRecordClient(h.conn).InitializeAttributes(ctx, &toyohrpb.InitializeAttributesRequest{
		User:      "tenants/current/users/current",
		InitialVs: &toyohrpb.InitializeAttributesRequest_InitialVs{SeasonId: 16, InitialRate: 1700},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := resp.GetAttributes().GetFields()
	for _, mode := range []string{"tw", "ar", "lf", "gl", "cl"} {
		if !f["mmr_initialized_"+mode].GetBooleanValue() || f["mmr_rate_"+mode].GetDoubleValue() != 1700 {
			t.Errorf("mode %s: initialized=%v rate=%v", mode, f["mmr_initialized_"+mode], f["mmr_rate_"+mode])
		}
	}
}
