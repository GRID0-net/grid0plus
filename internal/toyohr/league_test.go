package toyohr

import (
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// The captured working answer is the template: the event type rides in
// attributes.EventTypeId, slots are <id>_N, the name carries the request's
// target, and the timestamp is the zero time. Over events are not sent.
func TestAChallengeIsServedInTheCapturedShapeUntilItsLastSlotEnds(t *testing.T) {
	s := &Service{tenant: "t-test"}
	now := time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)
	slot := func(h int) db.LeagueSlot {
		return db.LeagueSlot{StartAt: now.Add(time.Duration(h) * time.Hour), EndAt: now.Add(time.Duration(h+2) * time.Hour)}
	}
	got := s.leagueSchedules([]db.LeagueEvent{
		{ID: "over", EventTypeID: "Old", Slots: []db.LeagueSlot{slot(-5)}},
		{ID: "ev1", EventTypeID: "TestEvent", Rule: 2, Stages: []int32{3, 4}, Slots: []db.LeagueSlot{slot(-1), slot(6)}},
	}, "default", now)
	if len(got) != 1 {
		t.Fatalf("served %d schedules, want only the one not over", len(got))
	}
	g := got[0]
	if g.GetName() != "tenants/t-test/targets/default/leagueSchedules/ev1" {
		t.Errorf("name = %q", g.GetName())
	}
	if g.GetAttributes().GetFields()["EventTypeId"].GetStringValue() != "TestEvent" || g.GetRule() != 2 || len(g.GetStages()) != 2 {
		t.Errorf("event = %v", g)
	}
	if g.GetTimestamp().GetSeconds() != -62135596800 {
		t.Errorf("timestamp = %v, want the zero time", g.GetTimestamp())
	}
	if len(g.GetSlots()) != 2 || g.GetSlots()[1].GetName() != "ev1_2" ||
		!g.GetStartTime().AsTime().Equal(now.Add(-time.Hour)) || !g.GetEndTime().AsTime().Equal(now.Add(8*time.Hour)) {
		t.Errorf("slots = %v", g.GetSlots())
	}
}
