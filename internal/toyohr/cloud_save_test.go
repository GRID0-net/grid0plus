package toyohr

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// TestGetSaveRecordAnswersNotFoundRatherThanFabricatingASave is CLAUDE.md's
// "no game dump, no save data" boundary applied to a boot query: a fresh
// account has no cloud save on a real server either, and NotFound is what
// the game already has to handle for that case -- not a placeholder for
// missing functionality.
func TestGetSaveRecordAnswersNotFoundRatherThanFabricatingASave(t *testing.T) {
	svc, _ := newService(t, time.Now())

	_, err := svc.GetSaveRecord(context.Background(), &toyohrpb.GetSaveRecordRequest{
		Name: "tenants/" + testTenant + "/users/aaaaaaaaaaaaaaaa/saveRecord",
	})
	if err == nil {
		t.Fatal("GetSaveRecord succeeded for an account with no cloud save")
	}
	if status.Code(err) != codes.NotFound {
		t.Errorf("code = %v, want NotFound", status.Code(err))
	}
}
