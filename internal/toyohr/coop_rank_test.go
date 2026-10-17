package toyohr

import (
	"context"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/ugcstore"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// The game sends coop_shift several times for the shift it is in, and once
// more for each new one: only the new one may reset the points.
func TestOnlyANewRotationResetsTheSalmonRunPoints(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	svc, _ := newService(t, now)
	svc.docs = ugcstore.NewService(ugcstore.Options{
		Store: svc.store, Log: discardLogger(), Tenant: testTenant,
		CallerID: func(context.Context) (string, bool) { return testCaller, true },
		Now:      func() time.Time { return now },
	})
	g := gameRecordService{Service: svc}
	ctx := context.Background()
	open := func(shift time.Time) {
		t.Helper()
		if _, err := g.InitializeAttributes(ctx, &toyohrpb.InitializeAttributesRequest{
			User:      "tenants/current/users/current",
			CoopShift: &toyohrpb.InitializeAttributesRequest_CoopShift{ShiftId: CoopShiftID(shift), JobType: "Normal"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	shifts := liveNormalCoopShifts(now, 2)
	open(shifts[0].Start)
	if _, _, err := ugcstore.CreditLANCoopGrade(ctx, svc.store, testTenant, "j1", testCaller, 20, now); err != nil {
		t.Fatal(err)
	}
	open(shifts[0].Start)
	total, _, err := ugcstore.CoopGradePoint(ctx, svc.store, testTenant, testCaller)
	if err != nil || total != 20 {
		t.Fatalf("total = %d (%v) after a repeated coop_shift, want 20 kept", total, err)
	}
	open(shifts[1].Start)
	if total, _, _ = ugcstore.CoopGradePoint(ctx, svc.store, testTenant, testCaller); total != 40 {
		t.Fatalf("total = %d in a new rotation, want Apprentice 40", total)
	}
}
