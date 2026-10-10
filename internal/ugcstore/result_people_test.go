package ugcstore

import (
	"context"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	"google.golang.org/protobuf/proto"
)

func TestALaterIndividualReportKeepsEveryPlayerInBattleHistory(t *testing.T) {
	mapValue := func(fields map[string]*commonpb.Value) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: fields}}}
	}
	alice := mapValue(map[string]*commonpb.Value{"npln_user_id": str("alice"), "team": integer(1)})
	bob := mapValue(map[string]*commonpb.Value{"npln_user_id": str("bob"), "team": integer(2)})
	verdict := &commonpb.MapValue{Fields: map[string]*commonpb.Value{"personal_result": mapValue(map[string]*commonpb.Value{"a": alice, "b": bob})}}
	for _, mode := range []string{"VsResults", "CoopResults"} {
		t.Run(mode, func(t *testing.T) {
			store := db.NewMemory()
			ctx := context.Background()
			now := time.Now()
			battle := "20261008T120000_room"
			for _, entry := range []struct {
				key   string
				value *commonpb.Value
			}{{"a", alice}, {"b", bob}} {
				report := &commonpb.MapValue{Fields: map[string]*commonpb.Value{"personal_result": mapValue(map[string]*commonpb.Value{entry.key: entry.value})}}
				var err error
				if mode == "VsResults" {
					err = RecordVsResults(ctx, store, "dce9377b", battle, []string{"alice", "bob"}, report, verdict, nil, nil, now)
				} else {
					err = RecordCoopResults(ctx, store, "dce9377b", battle, report, verdict, nil, nil, now)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, uid := range []string{"alice", "bob"} {
				row, err := store.GetUGCDocument(ctx, "tenants/dce9377b/documents/services/GameRecord/users/"+uid+"/"+mode+"/"+battle)
				if err != nil {
					t.Fatal(err)
				}
				fields := new(commonpb.MapValue)
				if err := proto.Unmarshal(row.Fields, fields); err != nil {
					t.Fatal(err)
				}
				var values []*commonpb.Value
				if mode == "VsResults" {
					values = append(values, fields.Fields["personal_data_team1"].GetArrayValue().GetValues()...)
					values = append(values, fields.Fields["personal_data_team2"].GetArrayValue().GetValues()...)
				} else {
					values = fields.Fields["personal_data"].GetArrayValue().GetValues()
				}
				seen := map[string]bool{}
				for _, value := range values {
					seen[value.GetMapValue().GetFields()["npln_user_id"].GetStringValue()] = true
				}
				if len(values) != 2 || !seen["alice"] || !seen["bob"] {
					t.Fatalf("%s history lost players: %v", uid, seen)
				}
			}
		})
	}
}
