package ugcstore

import (
	"context"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
)

func TestAFinishedSalmonRunJobFillsTheBonusMeterOnce(t *testing.T) {
	store := db.NewMemory()
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 16, 0, 0, 0, time.UTC)
	m := func(f map[string]*commonpb.Value) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: f}}}
	}
	verdict := &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"shift_id":   str("20261003080000"),
		"shift_time": str("20261003080000-20261004160000"),
		"team_result": m(map[string]*commonpb.Value{
			"final_wave": integer(2),
			"waves": {ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.Value{
				m(map[string]*commonpb.Value{"delivered_golden_ikura_num": integer(20)}),
				m(map[string]*commonpb.Value{"delivered_golden_ikura_num": integer(25)}),
			}}}},
		}),
		"personal_result": m(map[string]*commonpb.Value{
			"us-alice": m(map[string]*commonpb.Value{"golden_ikura_num": integer(12), "ikura_num": integer(300)}),
			"us-bob":   m(map[string]*commonpb.Value{"golden_ikura_num": integer(9), "rescue_num": integer(2)}),
		}),
	}}
	users := map[string]string{"us-alice": "alice", "us-bob": "bob"}

	// Every console reports the same job; it must count once.
	for i := 0; i < 3; i++ {
		if err := RecordCoopPointCards(ctx, store, "dce9377b", "20261003T160000_job", verdict, users, now); err != nil {
			t.Fatal(err)
		}
	}

	read := func(name string) map[string]*commonpb.Value {
		t.Helper()
		d, err := store.GetUGCDocument(ctx, "tenants/dce9377b/documents/services/GameRecord/users/"+name)
		if err != nil {
			t.Fatalf("%s: %v -- the game reads this and never writes it", name, err)
		}
		f, err := decodeFields(d.Fields)
		if err != nil {
			t.Fatal(err)
		}
		return f.GetFields()
	}
	total := read("alice/PointCardTotal/Data")
	// 45 delivered by the team, cleared: 2 x 45 + 50.
	if got := total["kuma_point"].GetIntegerValue(); got != 140 {
		t.Errorf("kuma_point = %d, want 140, credited once", got)
	}
	if got := total["job_num"].GetIntegerValue(); got != 1 {
		t.Errorf("job_num = %d, want 1", got)
	}
	if got := total["golden_ikura_total"].GetIntegerValue(); got != 12 {
		t.Errorf("golden_ikura_total = %d, want alice's own 12", got)
	}
	if got := total["version"].GetIntegerValue(); got != pointCardVersion {
		t.Errorf("version = %d", got)
	}
	shift := read("bob/PointCardRegular/20261003080000")
	if got := shift["kuma_point"].GetIntegerValue(); got != 140 {
		t.Errorf("bob's shift kuma_point = %d, want the team's 140", got)
	}
	if got := shift["rescue_total"].GetIntegerValue(); got != 2 {
		t.Errorf("bob's rescue_total = %d, want 2", got)
	}
}
