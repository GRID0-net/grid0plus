package ugcstore

import (
	"context"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
)

func TestSmallCoopJobsAdvanceGradeAndRetriesDoNotCreditTwice(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		result, wave, before, want int64
		disconnect                 bool
	}{
		{"clear", 0, 2, 240, 260, false}, {"wave one loss", 1, 0, 240, 220, false},
		{"wave two loss", 1, 1, 240, 230, false}, {"wave three loss", 1, 2, 240, 240, false},
		{"real disconnect", 1, 0, 240, 230, true}, {"promotion", 0, 2, 280, 340, false},
		{"demotion", 1, 0, 300, 240, false}, {"VP cap", 0, 2, 1790, 1799, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := db.NewMemory()
			now := time.Now().UTC()
			name := "tenants/t-dce9377b-lp1/documents/services/GameRecord/users/alice/CoopUserAttribute/Data"
			encoded, _ := encodeFields(&commonpb.MapValue{Fields: map[string]*commonpb.Value{"total_grade_point": integer(tc.before), "smell": integer(8)}})
			if err := store.PutUGCDocument(ctx, db.UGCDocument{Name: name, Owner: "alice", Fields: encoded, CreateTime: now, UpdateTime: now}); err != nil {
				t.Fatal(err)
			}
			m := func(f map[string]*commonpb.Value) *commonpb.Value {
				return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: f}}}
			}
			verdict := &commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"team_result":     m(map[string]*commonpb.Value{"result": integer(tc.result), "final_wave": integer(tc.wave)}),
				"personal_result": m(map[string]*commonpb.Value{"a": m(map[string]*commonpb.Value{}), "b": m(map[string]*commonpb.Value{"disconnected": {ValueType: &commonpb.Value_BooleanValue{BooleanValue: tc.disconnect}}})}),
			}}
			for i := 0; i < 3; i++ {
				if err := ApplyCoopGrades(ctx, store, "t-dce9377b-lp1", "job", "coop_regular_config", verdict, map[string]string{"a": "alice", "b": "bob"}, now); err != nil {
					t.Fatal(err)
				}
			}
			d, err := store.GetUGCDocument(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			attrs, err := decodeFields(d.Fields)
			if err != nil {
				t.Fatal(err)
			}
			if got := attrs.Fields["total_grade_point"].GetIntegerValue(); got != tc.want {
				t.Fatalf("grade=%d, want %d", got, tc.want)
			}
			if got := attrs.Fields["smell"].GetIntegerValue(); got != 8 {
				t.Fatalf("unrelated attribute overwritten")
			}
			p := verdict.Fields["personal_result"].GetMapValue().Fields["a"].GetMapValue().Fields
			if p["grade_point_before"].GetIntegerValue() != tc.before || p["grade_point_after"].GetIntegerValue() != tc.want {
				t.Fatal("retry changed the job's before/after grade")
			}
			if tc.disconnect && verdict.Fields["personal_result"].GetMapValue().Fields["b"].GetMapValue().Fields["grade_point_after"].GetIntegerValue() != 0 {
				t.Fatal("disconnected player received survivor credit")
			}
		})
	}
}
