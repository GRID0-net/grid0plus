package gamesync

import (
	"reflect"
	"sort"
	"testing"
	"time"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

func TestPrivateRoomDocumentsKeepCapturedFixedAndMutableShapes(t *testing.T) {
	s := &Server{docs: newStore(time.Now)}
	caller := &session{details: &SessionDetails{
		UserID: "alice", SessionID: "room-id", Tenant: "t-dce9377b-lp1", Mode: "private_match_config",
		Host: "example.invalid", Port: 443, Capacity: 10, ParticipantCount: 1,
		CreatedAt: time.Unix(1000, 0),
	}, userSession: "gameSessions/room-id/userSessions/us-alice"}
	keys := func(fields map[string]*commonpb.Value) []string {
		out := make([]string, 0, len(fields))
		for k := range fields {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	wantKeys := func(name string, fields map[string]*commonpb.Value, want []string) {
		t.Helper()
		sort.Strings(want)
		if got := keys(fields); !reflect.DeepEqual(got, want) {
			t.Errorf("%s fields = %v, want captured %v", name, got, want)
		}
	}
	fixed, ok := s.roomDocument(caller, "docs/__gs/f")
	if !ok {
		t.Fatal("missing fixed room document")
	}
	wantKeys("__gs/f", fixed.GetFields().GetFields(), []string{"addr", "gsid", "maxu", "mcn", "p", "rs", "tid"})
	if got := len(fixed.GetFields().GetFields()["rs"].GetBytesValue()); got != 32 {
		t.Errorf("fixed rs length = %d, want captured 32", got)
	}
	if got := fixed.GetFields().GetFields()["maxu"].GetIntegerValue(); got != 8 {
		t.Errorf("fixed maxu = %d, want captured 8", got)
	}
	if got := fixed.GetFields().GetFields()["tid"].GetStringValue(); got != caller.details.Tenant {
		t.Errorf("fixed tid = %q, want tenant", got)
	}
	mutable, ok := s.roomDocument(caller, "docs/__stg/All")
	if !ok {
		t.Fatal("missing mutable room document")
	}
	rs := mutable.GetFields().GetFields()["rs"].GetMapValue().GetFields()
	if got := rs["bfmax"].GetIntegerValue(); got != 8 {
		t.Errorf("mutable bfmax = %d, want captured 8", got)
	}
	if len(rs["prp"].GetMapValue().GetFields()) != 0 {
		t.Errorf("mutable properties = %v, want empty map", rs["prp"])
	}
	settings, _ := s.roomDocument(caller, "docs/__gs/m")
	if settings.GetFields().GetFields()["prp"].GetMapValue() == nil {
		t.Error("room settings omit the captured prp map")
	}
	wantKeys("__gs/m", settings.GetFields().GetFields(), []string{"bfmax", "bfmin", "cp", "ebf", "ip", "prp", "pw"})
	master := s.collectionSnapshot(caller, "docs/MasterCollection")
	if len(master) != 1 || master[0].GetName() != "docs/MasterCollection/Session" ||
		master[0].GetFields().GetFields()["id"].GetStringValue() != "" {
		t.Fatalf("room's MasterCollection snapshot = %v", master)
	}
	if got := settings.GetFields().GetFields()["bfmax"].GetIntegerValue(); got != 5 {
		t.Errorf("__gs/m bfmax = %d, want captured 5", got)
	}
	status, _ := s.roomDocument(caller, "docs/__gs/s")
	wantKeys("__gs/s", status.GetFields().GetFields(), []string{"att", "ltc", "pgn", "st", "tn", "ucsid", "uid", "upcsid", "ussid"})
	if got := status.GetFields().GetFields()["uid"].GetStringValue(); got != "alice" {
		t.Errorf("__gs/s uid = %q", got)
	}
	_, _, err := s.docs.apply([]*syncpb.WriteOperation{{OperationType: &syncpb.WriteOperation_MergeDocument{
		MergeDocument: &syncpb.MergeDocumentRequest{Document: &syncpb.Document{
			Name: "docs/__us/us-alice", Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"uid":   {ValueType: &commonpb.Value_StringValue{StringValue: "mallory"}},
				"ucsid": {ValueType: &commonpb.Value_IntegerValue{IntegerValue: 7}},
			}}},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	status, _ = s.roomDocument(caller, "docs/__gs/s")
	if got := status.GetFields().GetFields()["ucsid"].GetIntegerValue(); got != 7 {
		t.Errorf("__gs/s ignored the client's current session counter: %d", got)
	}
	if got := status.GetFields().GetFields()["uid"].GetStringValue(); got != "alice" {
		t.Errorf("__gs/s trusted a client-supplied identity: %q", got)
	}
}
