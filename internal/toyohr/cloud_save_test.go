package toyohr

import (
	"context"
	"testing"
	"time"

	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// TestGetSaveRecordNeverAnswersNotFoundOrEmpty exercises the direct Go
// entry point Service.GetSaveRecord, not the wire: a real console never
// reaches this method (registerCloudSave's stream handler intercepts
// GetSaveRecord before Go-level dispatch and answers with no message at
// all -- see getSaveRecordStreamHandler's own doc comment for why that
// differs from this and is the behaviour actually measured against real
// Nintendo servers). This direct path stays exercised because tests and
// internal/npln's diagnostic probe still call it directly.
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
