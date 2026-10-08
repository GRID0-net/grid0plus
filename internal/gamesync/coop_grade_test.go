package gamesync

import (
	"context"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/ugcstore"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"google.golang.org/protobuf/proto"
)

func TestFinalCoopVerdictPublishesPersistedGradesWithoutDoubleCredit(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	h.srv.mu.Lock()
	for _, sess := range h.srv.sessions {
		sess.details = &SessionDetails{UserID: "alice", SessionID: "job", Mode: "coop_regular_config"}
	}
	h.srv.mu.Unlock()
	store := db.NewMemory()
	const tenant = "t-dce9377b-lp1"
	const name = "tenants/" + tenant + "/documents/services/GameRecord/users/alice/CoopUserAttribute/Data"
	encoded, _ := proto.Marshal(&commonpb.MapValue{Fields: map[string]*commonpb.Value{"total_grade_point": intValue(240)}})
	now := time.Now().UTC()
	if err := store.PutUGCDocument(context.Background(), db.UGCDocument{Name: name, Owner: "alice", Fields: encoded, CreateTime: now, UpdateTime: now}); err != nil {
		t.Fatal(err)
	}
	h.srv.prepareCoopVerdict = func(ctx context.Context, r RefereeReport) error {
		return ugcstore.ApplyCoopGrades(ctx, store, tenant, r.BattleID, r.Mode, r.Verdict, r.SessionUsers, now)
	}
	m := func(f map[string]*commonpb.Value) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: f}}}
	}
	const battle = "20261008T160000_job"
	write := func(name string, f map[string]*commonpb.Value) {
		t.Helper()
		if _, err := h.client.WriteDocuments(alice, &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{merge(doc(name, f))}}); err != nil {
			t.Fatal(err)
		}
	}
	write(refereeRequestPrefix+battle, map[string]*commonpb.Value{"shift_id": verdictString("shift"), "users": {ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.Value{verdictString("alice")}}}}})
	for i := 0; i < 2; i++ {
		write(refereeReportPrefix+battle+"/User/token-alice", map[string]*commonpb.Value{"team_result": m(map[string]*commonpb.Value{"result": intValue(0), "final_wave": intValue(2)}), "personal_result": m(map[string]*commonpb.Value{"token-alice": m(map[string]*commonpb.Value{})})})
		got, err := h.client.GetDocument(alice, &syncpb.GetDocumentRequest{Name: refereeResultPrefix + battle})
		if err != nil {
			t.Fatal(err)
		}
		personal := got.Fields.Fields["personal_result"].GetMapValue().Fields["token-alice"].GetMapValue().Fields
		if personal["grade_point_before"].GetIntegerValue() != 240 || personal["grade_point_after"].GetIntegerValue() != 260 {
			t.Fatal("published verdict does not carry stable before/after grades")
		}
		d, err := store.GetUGCDocument(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		attrs := &commonpb.MapValue{}
		if err := proto.Unmarshal(d.Fields, attrs); err != nil {
			t.Fatal(err)
		}
		if attrs.Fields["total_grade_point"].GetIntegerValue() != 260 {
			t.Fatal("published result precedes stored grade or retry credited twice")
		}
	}
}
