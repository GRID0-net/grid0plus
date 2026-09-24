package toyohr

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/n-popescu/switchnet/internal/db"
	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

func TestEmptyFestProbeIsLimitedToOneAuthenticatedPlayer(t *testing.T) {
	caller := "probe-player"
	svc := NewService(Options{
		Store: db.NewMemory(), Log: discardLogger(), Tenant: testTenant,
		Now:                     func() time.Time { return time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC) },
		CallerID:                func(context.Context) (string, bool) { return caller, caller != "" },
		DiagnosticEmptyFestUser: "probe-player",
	})
	get := func() *toyohrpb.SelectFestScheduleResponse {
		t.Helper()
		resp, err := svc.SelectFestSchedule(context.Background(),
			&toyohrpb.SelectFestScheduleRequest{Tenant: testTenant, Region: "EU"})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	b, err := proto.Marshal(get())
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 0 {
		t.Fatalf("probe response has %d protobuf body bytes, want the captured empty message", len(b))
	}

	caller = "another-player"
	if get().GetSchedule() == nil {
		t.Fatal("another player lost the normal sentinel")
	}
	caller = ""
	if get().GetSchedule() == nil {
		t.Fatal("unauthenticated caller received the diagnostic response")
	}

	// A configured fest, when one exists, always takes precedence over this
	// probe: the opt-in compares only the otherwise-sentinel branch.
	caller = "probe-player"
	fest := NewFest(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), nil)
	if err := svc.SaveFest(context.Background(), fest, true, nil); err != nil {
		t.Fatal(err)
	}
	if get().GetSchedule() == nil {
		t.Fatal("the probe hid an active operator fest")
	}
}
