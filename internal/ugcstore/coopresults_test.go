package ugcstore

import (
	"context"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"google.golang.org/protobuf/proto"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
)

// TestAFinishedJobAppearsInEachPlayersJobHistory: Salmon Run's history reads
// services/GameRecord/users/<uid>/CoopResults; with nothing there it is empty.
// The reader (0x710383a0b0) takes the shift from the top level, the scenario
// as one map, and each player's name and weapon from
// personal_data[i].personal_profile.player_info.
func TestAFinishedJobAppearsInEachPlayersJobHistory(t *testing.T) {
	store := db.NewMemory()
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 19, 0, 0, 0, time.UTC)
	mapOf := func(f map[string]*commonpb.Value) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: f}}}
	}
	waves := &commonpb.Value{ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.Value{
		mapOf(map[string]*commonpb.Value{"wave": integer(1), "golden_ikura_num": integer(7)}),
	}}}}
	report := &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"team_result": mapOf(map[string]*commonpb.Value{
			"final_wave": integer(2), "result": integer(1),
			"wave_detail": mapOf(map[string]*commonpb.Value{}),
		}),
		"personal_result": mapOf(map[string]*commonpb.Value{
			"session-a": mapOf(map[string]*commonpb.Value{"npln_user_id": str("alice"), "personal_wave_result": waves}),
			"session-b": mapOf(map[string]*commonpb.Value{"personal_wave_result": waves}),
		}),
	}}
	verdict := &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"shift_id": str("20261006000000"),
		"scenario": mapOf(map[string]*commonpb.Value{"stage": integer(2), "smell_num": integer(8)}),
	}}
	profiles := map[string]*commonpb.Value{"alice": mapOf(map[string]*commonpb.Value{"name": str("Alice")})}
	if err := RecordCoopResults(ctx, store, "dce9377b", "20261006T190000_j", report, verdict,
		map[string]string{"session-b": "bob"}, profiles, now); err != nil {
		t.Fatalf("RecordCoopResults: %v", err)
	}
	for _, uid := range []string{"alice", "bob"} {
		doc, err := store.GetUGCDocument(ctx, "tenants/dce9377b/documents/services/GameRecord/users/"+uid+"/CoopResults/20261006T190000_j")
		if err != nil {
			t.Fatalf("%s's job: %v", uid, err)
		}
		var f commonpb.MapValue
		if err := proto.Unmarshal(doc.Fields, &f); err != nil {
			t.Fatal(err)
		}
		if got := f.Fields["shift_id"].GetStringValue(); got != "20261006000000" {
			t.Errorf("shift_id = %q", got)
		}
		if got := f.Fields["scenario"].GetMapValue().GetFields()["stage"].GetIntegerValue(); got != 2 {
			t.Errorf("scenario.stage = %d, want 2", got)
		}
		if got := f.Fields["final_wave"].GetIntegerValue(); got != 2 {
			t.Errorf("final_wave = %d, want the team result's 2", got)
		}
		if n := len(f.Fields["personal_data"].GetArrayValue().GetValues()); n != 2 {
			t.Errorf("personal_data has %d players, want 2", n)
		}
	}
	doc, _ := store.GetUGCDocument(ctx, "tenants/dce9377b/documents/services/GameRecord/users/alice/CoopResults/20261006T190000_j")
	var f commonpb.MapValue
	_ = proto.Unmarshal(doc.Fields, &f)
	for _, p := range f.Fields["personal_data"].GetArrayValue().GetValues() {
		pf := p.GetMapValue().GetFields()
		if pf["npln_user_id"].GetStringValue() != "alice" {
			continue
		}
		name := pf["personal_profile"].GetMapValue().GetFields()["player_info"].GetMapValue().GetFields()["name"].GetStringValue()
		if name != "Alice" {
			t.Errorf("personal_profile.player_info.name = %q, want Alice", name)
		}
		return
	}
	t.Error("alice is not in personal_data")
}
