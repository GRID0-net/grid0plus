package dashboard

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/toyohr"
)

func TestScheduleViewerServesTheSameVsAndCoopContentAsTheWire(t *testing.T) {
	now := time.Now().UTC()
	store := db.NewMemory()
	srv, err := New(Options{
		Store:  store,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tenant: "dce9377b",
		Now:    func() time.Time { return now },
		Rotation: toyohr.Rotation{
			RegularStages: []int32{1, 2}, StagesPerSlot: 2,
		},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/schedule-viewer", nil))
	if rec.Code != 200 {
		t.Fatalf("schedule-viewer: %d %s", rec.Code, rec.Body)
	}

	var body struct {
		Vs   []vsSlotRow    `json:"vs"`
		Coop []coopShiftRow `json:"coop"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Vs) == 0 {
		t.Error("got no Vs slots with a configured rotation")
	}
	if got := body.Vs[0].Regular; len(got) == 0 || got[0].Name == "" {
		t.Errorf("first Vs slot's regular stages = %+v, want real names", got)
	}
	// A fresh install has no stored coop shifts, so every row here must be
	// live-filled -- the whole point of liveNormalCoopShifts.
	if len(body.Coop) == 0 {
		t.Error("got no coop shifts with nothing stored -- live fill did not run")
	}
	for _, row := range body.Coop {
		if !row.Generated {
			t.Errorf("row %q is not marked generated, but nothing was stored", row.ID)
		}
	}
}
