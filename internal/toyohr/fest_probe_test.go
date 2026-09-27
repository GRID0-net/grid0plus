package toyohr

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/n-popescu/grid0plus/internal/db"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

func TestNoFestResponseIsEmptyForEveryPlayer(t *testing.T) {
	svc := NewService(Options{
		Store: db.NewMemory(), Log: discardLogger(), Tenant: testTenant,
		Now: func() time.Time { return time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC) },
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

	for _, caller := range []string{"probe-player", "another-player"} {
		b, err := proto.Marshal(get())
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != 0 {
			t.Fatalf("%s: no-fest response has %d protobuf body bytes, want the captured empty message", caller, len(b))
		}
	}

	// A configured fest is still announced while its publication window is open.
	fest := NewFest(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), nil)
	if err := svc.SaveFest(context.Background(), fest, true, nil); err != nil {
		t.Fatal(err)
	}
	if get().GetSchedule() == nil {
		t.Fatal("an active operator fest was not announced")
	}
}
