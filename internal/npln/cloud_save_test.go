package npln

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

func strV(s string) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: s}}
}

// TestACloudSaveTheConsoleWroteComesBackOnTheNextBoot is the round trip a
// console needs to ever hold a resolved cloud save: an established console
// told "nothing in the cloud" sends evt=Validate with the keys it is missing
// (captured 2026-09-24), and the next boot's GetSaveRecord must return them
// instead of the zero-message answer again.
func TestACloudSaveTheConsoleWroteComesBackOnTheNextBoot(t *testing.T) {
	const uid = "save000000000001"
	h := newHarness(t)
	h.createUser(uid, "device-save-1")
	saves := toyohrpb.NewCloudSaveClient(h.conn)
	ctx := h.ctx(h.sessionFor(t, uid, "device-save-1"), "")
	get := &toyohrpb.GetSaveRecordRequest{Name: "tenants/current/saveRecords/current"}

	// An established console only writes after seeing an existing, empty
	// record (2026-09-24 hardware); zero messages made it write nothing.
	if empty, err := saves.GetSaveRecord(ctx, get); err != nil || len(empty.GetSaveData().GetFields()) != 0 || empty.GetName() == "" {
		t.Fatalf("first boot GetSaveRecord = %v, %v; want a named, empty record", empty, err)
	}

	first, err := saves.WriteSaveRecord(ctx, &toyohrpb.WriteSaveRecordRequest{
		Name:          "tenants/current/saveRecords/current",
		SaveEventType: "tenants/current/saveEventTypes/Validate",
		SaveRecord: &toyohrpb.SaveRecord{SaveData: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"HaveGearHeadMap": {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{
				Fields: map[string]*commonpb.Value{"1000": intV(1)}}}},
		}}},
	})
	if err != nil {
		t.Fatalf("WriteSaveRecord: %v", err)
	}
	second, err := saves.WriteSaveRecord(ctx, &toyohrpb.WriteSaveRecordRequest{
		Name:          "tenants/current/saveRecords/current",
		UpdateTime:    first.GetTimestamp(),
		SaveEventType: "tenants/current/saveEventTypes/NewSeason",
		SaveRecord: &toyohrpb.SaveRecord{SaveData: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"Season": {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{
				Fields: map[string]*commonpb.Value{"CurrentSeasonId": intV(16)}}}},
		}}},
	})
	if err != nil {
		t.Fatalf("second WriteSaveRecord: %v", err)
	}
	if !second.GetTimestamp().AsTime().After(first.GetTimestamp().AsTime()) {
		t.Errorf("version token did not advance: %v then %v", first.GetTimestamp().AsTime(), second.GetTimestamp().AsTime())
	}

	got, err := saves.GetSaveRecord(ctx, get)
	if err != nil {
		t.Fatalf("next boot GetSaveRecord: %v, want the stored record", err)
	}
	f := got.GetSaveData().GetFields()
	if f["HaveGearHeadMap"].GetMapValue().GetFields()["1000"].GetIntegerValue() != 1 {
		t.Errorf("Validate delta missing from the stored record: %v", f)
	}
	if f["Season"].GetMapValue().GetFields()["CurrentSeasonId"].GetIntegerValue() != 16 {
		t.Errorf("NewSeason delta missing from the stored record: %v", f)
	}
	if want := "tenants/" + h.srv.tenant + "/saveRecords/" + uid; got.GetName() != want {
		t.Errorf("name = %q, want %q", got.GetName(), want)
	}
	if !got.GetUpdateTime().AsTime().Equal(second.GetTimestamp().AsTime()) {
		t.Errorf("update_time = %v, want the last write's token %v", got.GetUpdateTime().AsTime(), second.GetTimestamp().AsTime())
	}
}

// TestOneConsoleCannotReadOrOverwriteAnothersCloudSave holds CloudSave to the
// fail-closed identity rule: the record is always the authenticated
// caller's, whatever name the request carries.
func TestOneConsoleCannotReadOrOverwriteAnothersCloudSave(t *testing.T) {
	const alice, bob = "save00000000000a", "save00000000000b"
	h := newHarness(t)
	h.createUser(alice, "device-alice")
	h.createUser(bob, "device-bob")
	saves := toyohrpb.NewCloudSaveClient(h.conn)
	aliceCtx := h.ctx(h.sessionFor(t, alice, "device-alice"), "")
	bobCtx := h.ctx(h.sessionFor(t, bob, "device-bob"), "")

	if _, err := saves.WriteSaveRecord(aliceCtx, &toyohrpb.WriteSaveRecordRequest{
		Name:       "tenants/current/saveRecords/" + bob,
		SaveRecord: &toyohrpb.SaveRecord{SaveData: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"UserName": strV("alice")}}},
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := saves.GetSaveRecord(bobCtx, &toyohrpb.GetSaveRecordRequest{Name: "tenants/current/saveRecords/" + alice}); err != nil || len(got.GetSaveData().GetFields()) != 0 {
		t.Errorf("bob GetSaveRecord = %v, %v; want bob's own empty record: alice's write must not land on, or be readable by, bob", got, err)
	}
	got, err := saves.GetSaveRecord(aliceCtx, &toyohrpb.GetSaveRecordRequest{Name: "tenants/current/saveRecords/current"})
	if err != nil || got.GetSaveData().GetFields()["UserName"].GetStringValue() != "alice" {
		t.Errorf("alice GetSaveRecord = %v, %v; want her own record", got, err)
	}
}

// TestCreateSaveRecordStoresTheConsolesOwnRecord covers the fresh-account
// path measured against real Nintendo: nothing in the cloud, then the
// console uploads its local save through CreateSaveRecord.
func TestCreateSaveRecordStoresTheConsolesOwnRecord(t *testing.T) {
	const uid = "save000000000003"
	h := newHarness(t)
	h.createUser(uid, "device-save-3")
	saves := toyohrpb.NewCloudSaveClient(h.conn)
	ctx := h.ctx(h.sessionFor(t, uid, "device-save-3"), "")

	created, err := saves.CreateSaveRecord(ctx, &toyohrpb.CreateSaveRecordRequest{
		Parent: "tenants/current",
		SaveRecord: &toyohrpb.SaveRecord{
			Name:     "tenants/current/saveRecords/current",
			SaveData: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"UserName": strV("fresh"), "PlayerRank": intV(1)}},
		},
	})
	if err != nil {
		t.Fatalf("CreateSaveRecord: %v", err)
	}
	if created.GetUpdateTime() == nil || created.GetCreateTime() == nil {
		t.Error("created record has no timestamps")
	}
	got, err := saves.GetSaveRecord(ctx, &toyohrpb.GetSaveRecordRequest{Name: "tenants/current/saveRecords/current"})
	if err != nil || got.GetSaveData().GetFields()["UserName"].GetStringValue() != "fresh" {
		t.Errorf("GetSaveRecord after create = %v, %v", got, err)
	}
}

// TestARenameIsWhatTheNextBootReads: the save's UserName is the one name the
// game reads the player's pseudo from, so a rename must land there and the
// rest of the save must survive it.
func TestARenameIsWhatTheNextBootReads(t *testing.T) {
	h := newHarness(t)
	h.createUser("rename0000000001", "device-rename-1")
	ctx := h.ctx(h.sessionFor(t, "rename0000000001", "device-rename-1"), "")
	saves := toyohrpb.NewCloudSaveClient(h.conn)

	if _, err := saves.WriteSaveRecord(ctx, &toyohrpb.WriteSaveRecordRequest{
		SaveRecord: &toyohrpb.SaveRecord{SaveData: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"UserName": strV("before"), "Money": intV(100),
		}}},
	}); err != nil {
		t.Fatalf("WriteSaveRecord: %v", err)
	}
	resp, err := saves.ChangeUserName(ctx, &toyohrpb.ChangeUserNameRequest{UserName: "after"})
	if err != nil {
		t.Fatalf("ChangeUserName: %v", err)
	}
	if resp.GetTimestamp1() == nil {
		t.Error("the answer carries no version timestamp")
	}

	got, err := saves.GetSaveRecord(ctx, &toyohrpb.GetSaveRecordRequest{Name: "tenants/current/saveRecords/current"})
	if err != nil {
		t.Fatalf("GetSaveRecord: %v", err)
	}
	f := got.GetSaveData().GetFields()
	if f["UserName"].GetStringValue() != "after" {
		t.Errorf("UserName = %v, want the new name", f["UserName"])
	}
	if f["Money"].GetIntegerValue() != 100 {
		t.Errorf("the rename lost the rest of the save: %v", f)
	}

	if _, err := saves.ChangeUserName(ctx, &toyohrpb.ChangeUserNameRequest{UserName: "  "}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("an empty name: %v, want InvalidArgument", err)
	}
}
