package ugcstore

import (
	"context"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
)

const rankTenant = "t-dce9377b-lp1"

func putCoopAttributes(t *testing.T, store db.Store, uid string, f map[string]*commonpb.Value) {
	t.Helper()
	now := time.Now().UTC()
	encoded, _ := encodeFields(&commonpb.MapValue{Fields: f})
	if err := store.PutUGCDocument(context.Background(), db.UGCDocument{
		Name: coopAttributeName(rankTenant, uid), Owner: uid, Fields: encoded, CreateTime: now, UpdateTime: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func coopAttributes(t *testing.T, store db.Store, uid string) map[string]*commonpb.Value {
	t.Helper()
	d, err := store.GetUGCDocument(context.Background(), coopAttributeName(rankTenant, uid))
	if err != nil {
		t.Fatal(err)
	}
	f, err := decodeFields(d.Fields)
	if err != nil {
		t.Fatal(err)
	}
	return f.Fields
}

func TestANewRotationKeepsTheTitleAndResetsThePointsOnce(t *testing.T) {
	for _, tc := range []struct {
		name         string
		before, best int64
		want         int64
	}{
		{"Overachiever", 385, 0, 340},
		{"Eggsecutive VP, no badge", 1150, 399, 840},
		{"bronze badge", 1150, 400, 1000},
		{"silver badge", 1150, 600, 1100},
		{"gold badge", 1150, 999, 1200},
		{"a badge does nothing below Eggsecutive VP", 750, 999, 740},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := db.NewMemory()
			putCoopAttributes(t, store, "alice", map[string]*commonpb.Value{
				"total_grade_point": integer(tc.before), "shift_id": str("20261015160000"), "smell": integer(3),
			})
			for i := 0; i < 3; i++ {
				// The game sends coop_shift several times for one shift.
				if _, err := RollCoopRotation(ctx, store, rankTenant, "alice", "20261017080000", tc.best, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			f := coopAttributes(t, store, "alice")
			if got := f["total_grade_point"].GetIntegerValue(); got != tc.want {
				t.Fatalf("total_grade_point = %d, want %d", got, tc.want)
			}
			if f["shift_id"].GetStringValue() != "20261017080000" || f["smell"].GetIntegerValue() != 3 {
				t.Fatal("the reset did not record the shift, or touched the Salmometer")
			}
		})
	}
}

func TestAPlayerWithNoStoredShiftIsNotReset(t *testing.T) {
	ctx := context.Background()
	store := db.NewMemory()
	putCoopAttributes(t, store, "alice", map[string]*commonpb.Value{"total_grade_point": integer(385)})
	if _, err := RollCoopRotation(ctx, store, rankTenant, "alice", "20261017080000", 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := coopAttributes(t, store, "alice")["total_grade_point"].GetIntegerValue(); got != 385 {
		t.Fatalf("total_grade_point = %d, want 385 kept", got)
	}
}

// A LAN job credited in a rotation must survive the player's next online
// coop_shift for that same rotation.
func TestALANJobKeepsItsPointsWhenTheGameOpensTheSameRotation(t *testing.T) {
	ctx := context.Background()
	store := db.NewMemory()
	shift := "20261017080000"
	if _, err := RollCoopRotation(ctx, store, rankTenant, "alice", shift, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // a double-clicked Accept
		if _, _, err := CreditLANCoopGrade(ctx, store, rankTenant, "job1", "alice", 20, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RollCoopRotation(ctx, store, rankTenant, "alice", shift, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	f := coopAttributes(t, store, "alice")
	if got := f["total_grade_point"].GetIntegerValue(); got != 20 {
		t.Fatalf("total_grade_point = %d, want 20 (one credit, no reset)", got)
	}
	if _, ok := f["smell"]; ok {
		t.Fatal("a LAN job moved the Salmometer")
	}
}

func TestEggsecutiveVPResultsRecordTheShiftsBest(t *testing.T) {
	ctx := context.Background()
	store := db.NewMemory()
	putCoopAttributes(t, store, "alice", map[string]*commonpb.Value{
		"total_grade_point": integer(1380), "shift_id": str("20261017080000"),
	})
	for i, delta := range []int64{20, -20, -20} {
		if _, _, err := CreditLANCoopGrade(ctx, store, rankTenant, string(rune('a'+i)), "alice", delta, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	bests, err := CoopBests(ctx, store, rankTenant, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got := bests["20261017080000"]; got != 600 {
		t.Fatalf("best = %d, want 600 (1400 reached, then lost)", got)
	}
}
