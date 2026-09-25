package npln

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
	ugcpb "github.com/n-popescu/switchnet/proto/ugcstore/v1"
)

func TestPrivateSaveComparisonDoesNotReachAnotherAccount(t *testing.T) {
	const source = "source-user"
	const target = "ugc0000000000001"
	const other = "ugc0000000000002"
	folder := t.TempDir()
	write := func(name, variable string, msg proto.Message) {
		t.Helper()
		blob, err := proto.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(folder, name)
		if err := os.WriteFile(path, blob, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(variable, path)
	}
	write("save.pb", "SWITCHNET_NPLN_DIAGNOSTIC_SAVE_PATH", &toyohrpb.SaveRecord{
		Name:     "tenants/old/saveRecords/" + source,
		SaveData: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"PlayerRank": intV(9)}},
	})
	for _, tc := range []struct{ name, variable, kind string }{
		{"vs.pb", "SWITCHNET_NPLN_DIAGNOSTIC_VS_PATH", "VsUserAttribute"},
		{"coop.pb", "SWITCHNET_NPLN_DIAGNOSTIC_COOP_PATH", "CoopUserAttribute"},
	} {
		write(tc.name, tc.variable, &ugcpb.Document{
			Name:   "tenants/old/documents/services/GameRecord/users/" + source + "/" + tc.kind + "/Data",
			Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"owner": {ValueType: &commonpb.Value_StringValue{StringValue: "users/" + source}}}},
		})
	}
	t.Setenv("SWITCHNET_NPLN_DIAGNOSTIC_USER", target)
	h := newHarness(t)
	h.createUser(target, "device-ugc-1")
	h.createUser(other, "device-ugc-2")
	saves := toyohrpb.NewCloudSaveClient(h.conn)
	docs := ugcpb.NewUgcstoreClient(h.conn)
	for _, tc := range []struct {
		uid, device string
		diagnostic  bool
	}{
		{target, "device-ugc-1", true},
		{other, "device-ugc-2", false},
	} {
		ctx := h.ctx(h.sessionFor(t, tc.uid, tc.device), "")
		save, err := saves.GetSaveRecord(ctx, &toyohrpb.GetSaveRecordRequest{Name: "tenants/current/saveRecords/current"})
		if tc.diagnostic {
			if err != nil {
				t.Fatal(err)
			}
			if _, hasRank := save.GetSaveData().GetFields()["PlayerRank"]; !hasRank {
				t.Errorf("%s: diagnostic save missing PlayerRank", tc.uid)
			}
		} else if err != nil || len(save.GetSaveData().GetFields()) != 0 {
			// A non-diagnostic account with nothing stored gets its own
			// empty record, never the diagnostic's.
			t.Errorf("%s: GetSaveRecord = %v, %v; want an empty record", tc.uid, save, err)
		}
		name := "tenants/current/documents/services/GameRecord/users/" + tc.uid + "/VsUserAttribute/Data"
		doc, err := docs.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: name})
		if tc.diagnostic {
			if err != nil || doc.GetFields().GetFields()["owner"].GetStringValue() != "users/"+target {
				t.Errorf("target attribute mismatch: %v", err)
			}
		} else if status.Code(err) != codes.NotFound {
			t.Errorf("other account GetDocument=%v, want NotFound", err)
		}
	}
}
