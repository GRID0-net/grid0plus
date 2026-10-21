package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/toyohr"
)

func TestAChallengeIsScheduledOnTwoHourSlotsAndCanBeDeleted(t *testing.T) {
	now := time.Now().UTC()
	svc, store := newTestServer(t, "", now)
	h := svc.Handler()
	a, b := toyohr.VsStages[0].ID, toyohr.VsStages[1].ID
	slot := toyohr.AlignToSlot(now.Add(4 * time.Hour))
	post := func(body string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/challenges", strings.NewReader(body)))
		return rec.Code
	}
	if code := post(fmt.Sprintf(`{"eventTypeId":"TestEvent","rule":2,"stages":[%d,%d],"slots":[%q]}`, a, b, slot.Add(time.Hour).Format(time.RFC3339))); code != 400 {
		t.Errorf("an off-boundary slot was accepted: %d", code)
	}
	if code := post(fmt.Sprintf(`{"eventTypeId":"bad id!","rule":2,"stages":[%d,%d],"slots":[%q]}`, a, b, slot.Format(time.RFC3339))); code != 400 {
		t.Errorf("a malformed event type was accepted: %d", code)
	}
	if code := post(fmt.Sprintf(`{"eventTypeId":"TestEvent","rule":2,"stages":[%d,%d],"slots":[%q,%q]}`, a, b,
		slot.Add(4*time.Hour).Format(time.RFC3339), slot.Format(time.RFC3339))); code != 201 {
		t.Fatalf("create: %d", code)
	}
	events, _ := store.ListLeagueEvents(t.Context())
	if len(events) != 1 || len(events[0].ID) != 20 || len(events[0].Slots) != 2 ||
		!events[0].Slots[0].StartAt.Equal(slot) || !events[0].Slots[0].EndAt.Equal(slot.Add(2*time.Hour)) {
		t.Fatalf("stored = %+v", events)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/challenges", nil))
	var rows []challengeRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0].Status != "upcoming" {
		t.Errorf("list = %s", rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/challenges/"+events[0].ID, nil))
	if events, _ = store.ListLeagueEvents(t.Context()); rec.Code != 204 || len(events) != 0 {
		t.Errorf("delete: %d, left %d", rec.Code, len(events))
	}
}
