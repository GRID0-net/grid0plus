package toyohr

import (
	"context"
	"testing"
	"time"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// TestGetSaveRecordNeverAnswersNotFoundOrEmpty is the regression test for a
// real crash: NextendoNetwork/splatoon-3 measured (facts only, not its code)
// that NotFound makes the game loop on a communication error, and a
// genuinely empty success makes it stop dead -- no CreateSaveRecord, no
// FestSchedule, no documents. Reproduced on SwitchNet's own hardware on
// 2026-09-24: the console went silent immediately after this RPC. A real,
// resolved SaveRecord with an honestly-empty SaveData is answered instead.
func TestGetSaveRecordNeverAnswersNotFoundOrEmpty(t *testing.T) {
	as(t, "aaaaaaaaaaaaaaaa")
	svc, _ := newService(t, time.Now())

	got, err := svc.GetSaveRecord(context.Background(), &toyohrpb.GetSaveRecordRequest{
		Name: "tenants/" + testTenant + "/users/aaaaaaaaaaaaaaaa/saveRecord",
	})
	if err != nil {
		t.Fatalf("GetSaveRecord: %v -- this is what crashes a real console", err)
	}
	if got == nil {
		t.Fatal("no save record at all -- this is what crashes a real console")
	}
	if got.GetSaveData() == nil {
		t.Error("save_data is nil, want an empty-but-present MapValue")
	}
	if got.GetName() == "" {
		t.Error("name is empty")
	}
	if got.GetCreateTime() == nil || got.GetUpdateTime() == nil {
		t.Error("create_time/update_time are unset")
	}
}

// TestGetSaveRecordRefusesAnUnauthenticatedCaller keeps this RPC on the
// fail-closed identity rule the rest of the codebase is shaped around: see
// CLAUDE.md's "identity is fail-closed" section.
func TestGetSaveRecordRefusesAnUnauthenticatedCaller(t *testing.T) {
	as(t, "")
	svc, _ := newService(t, time.Now())

	_, err := svc.GetSaveRecord(context.Background(), &toyohrpb.GetSaveRecordRequest{})
	if err == nil {
		t.Fatal("GetSaveRecord succeeded with no authenticated caller")
	}
}

// TestWriteSaveRecordAcknowledgesRatherThanUnimplemented is the regression
// test for a real crash: a VM-side loopback capture on 2026-09-24 showed
// the console calling WriteSaveRecord twice immediately after receiving
// GetSaveRecord's blank record, getting Unimplemented both times, and going
// silent for good shortly after -- see WriteSaveRecord's own doc comment.
func TestWriteSaveRecordAcknowledgesRatherThanUnimplemented(t *testing.T) {
	as(t, "aaaaaaaaaaaaaaaa")
	svc, _ := newService(t, time.Now())

	got, err := svc.WriteSaveRecord(context.Background(), &toyohrpb.WriteSaveRecordRequest{
		Name: "tenants/" + testTenant + "/saveRecords/aaaaaaaaaaaaaaaa",
	})
	if err != nil {
		t.Fatalf("WriteSaveRecord: %v -- this is what stalls a real console", err)
	}
	if got.GetTimestamp() == nil {
		t.Error("timestamp is unset")
	}
}

func TestWriteSaveRecordRefusesAnUnauthenticatedCaller(t *testing.T) {
	as(t, "")
	svc, _ := newService(t, time.Now())

	_, err := svc.WriteSaveRecord(context.Background(), &toyohrpb.WriteSaveRecordRequest{})
	if err == nil {
		t.Fatal("WriteSaveRecord succeeded with no authenticated caller")
	}
}
