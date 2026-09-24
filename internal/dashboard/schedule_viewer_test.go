package dashboard

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/toyohr"
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
	// Coop's live-fill is currently disabled (toyohr.liveCoopFillEnabled,
	// see its own doc comment: a real hardware crash correlated with the
	// live-generated CoopSchedule's missing Attributes field), so a fresh
	// install with nothing stored shows no coop rows at all right now --
	// the same as before that feature existed.
	if len(body.Coop) != 0 {
		t.Errorf("got %d coop shifts with nothing stored and live-fill disabled, want 0", len(body.Coop))
	}
}
