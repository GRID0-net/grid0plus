package splatdiag

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
	ugcpb "github.com/n-popescu/grid0plus/proto/ugcstore/v1"
)

func diagnosticString(s string) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: s}}
}

func TestPrivateBundleIsBoundToOneAccountAndLeavesSourcesUntouched(t *testing.T) {
	const source = "source-user"
	const target = "target-user"
	folder := t.TempDir()
	write := func(name, env string, msg proto.Message) {
		t.Helper()
		blob, err := proto.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(folder, name)
		if err := os.WriteFile(path, blob, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(env, path)
	}
	write("save.pb", "SWITCHNET_NPLN_DIAGNOSTIC_SAVE_PATH", &toyohrpb.SaveRecord{
		Name: "tenants/old/saveRecords/" + source,
		SaveData: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"UserName": diagnosticString("source name"),
			"owner":    diagnosticString("users/" + source),
		}},
	})
	for _, tc := range []struct{ name, env, kind string }{
		{"vs.pb", "SWITCHNET_NPLN_DIAGNOSTIC_VS_PATH", "VsUserAttribute"},
		{"coop.pb", "SWITCHNET_NPLN_DIAGNOSTIC_COOP_PATH", "CoopUserAttribute"},
	} {
		write(tc.name, tc.env, &ugcpb.Document{
			Name:   "tenants/old/documents/services/GameRecord/users/" + source + "/" + tc.kind + "/Data",
			Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"owner": diagnosticString("users/" + source)}},
		})
	}
	t.Setenv("SWITCHNET_NPLN_DIAGNOSTIC_USER", target)
	bundle, err := FromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bundle.SaveFor("other", "test", "other", time.Now()); ok {
		t.Fatal("another account received the save")
	}
	if _, ok := bundle.AttributeFor("other", "tenants/test/documents/services/GameRecord/users/other/VsUserAttribute/Data", time.Now()); ok {
		t.Fatal("another account received an attribute")
	}
	rec, ok := bundle.SaveFor(target, "test", "target name", time.Now())
	if !ok {
		t.Fatal("target save missing")
	}
	if rec.GetName() != "tenants/test/saveRecords/"+target || rec.GetSaveData().GetFields()["owner"].GetStringValue() != "users/"+target || rec.GetSaveData().GetFields()["UserName"].GetStringValue() != "target name" {
		t.Fatal("save identity was not retargeted")
	}
	key := "tenants/test/documents/services/GameRecord/users/" + target + "/VsUserAttribute/Data"
	doc, ok := bundle.AttributeFor(target, key, time.Now())
	if !ok || doc.GetName() != key || doc.GetFields().GetFields()["owner"].GetStringValue() != "users/"+target {
		t.Fatal("attribute identity was not retargeted")
	}
	if _, ok := bundle.AttributeFor(target, "tenants/test/documents/services/Hammer/users/"+target+"/UserAttribute/Data", time.Now()); ok {
		t.Fatal("Hammer was replaced")
	}
	if bundle.save.GetName() != "tenants/old/saveRecords/"+source || bundle.vs.GetFields().GetFields()["owner"].GetStringValue() != "users/"+source {
		t.Fatal("source bundle was mutated")
	}
}
