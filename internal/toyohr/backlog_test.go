package toyohr

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// TestCanolaAndLockerSelectDocumentsAreEmpty covers the reasoning in
// backlog.go's package comment: GRID0+ has no UGC document store, so an
// honest query finds nothing, for either service.
func TestCanolaAndLockerSelectDocumentsAreEmpty(t *testing.T) {
	svc, _ := newService(t, time.Now())

	canola, err := canolaService{Service: svc}.SelectDocuments(context.Background(), &toyohrpb.SelectDocumentsRequest{})
	if err != nil {
		t.Fatalf("Canola.SelectDocuments: %v", err)
	}
	if len(canola.GetDocuments()) != 0 {
		t.Errorf("Canola.SelectDocuments = %v, want none", canola.GetDocuments())
	}

	locker, err := lockerService{Service: svc}.SelectDocuments(context.Background(), &toyohrpb.SelectDocumentsRequest{})
	if err != nil {
		t.Fatalf("Locker.SelectDocuments: %v", err)
	}
	if len(locker.GetDocuments()) != 0 {
		t.Errorf("Locker.SelectDocuments = %v, want none", locker.GetDocuments())
	}
}

func TestCanolaAndLockerIkasuDocumentIsNotFound(t *testing.T) {
	svc, _ := newService(t, time.Now())

	if _, err := (canolaService{Service: svc}).IkasuDocument(context.Background(), &toyohrpb.IkasuDocumentRequest{}); status.Code(err) != codes.NotFound {
		t.Errorf("Canola.IkasuDocument = %v, want NotFound", err)
	}
	if _, err := (lockerService{Service: svc}).IkasuDocument(context.Background(), &toyohrpb.IkasuDocumentRequest{}); status.Code(err) != codes.NotFound {
		t.Errorf("Locker.IkasuDocument = %v, want NotFound", err)
	}
}

func TestCoopScenarioAndReplayCodesAreNeverFound(t *testing.T) {
	svc, _ := newService(t, time.Now())

	if _, err := (coopScenarioService{Service: svc}).ResolveCoopScenarioCode(
		context.Background(), &toyohrpb.ResolveCoopScenarioCodeRequest{Name: "ABCDEF"},
	); status.Code(err) != codes.NotFound {
		t.Errorf("ResolveCoopScenarioCode = %v, want NotFound", err)
	}
	if _, err := (replayService{Service: svc}).ResolveReplayCode(
		context.Background(), &toyohrpb.ResolveReplayCodeRequest{Name: "ABCDEF"},
	); status.Code(err) != codes.NotFound {
		t.Errorf("ResolveReplayCode = %v, want NotFound", err)
	}
}

// TestFulfillReservationsIsEmptyNotAnError draws the same distinction
// ListDeviceAccounts (empty list) and GetDeviceAccount (NotFound) already
// draw in internal/db: a query over many things that happen to be zero is
// not the same case as one specific thing that does not exist.
func TestFulfillReservationsIsEmptyNotAnError(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	svc, _ := newService(t, now)

	resp, err := (replayService{Service: svc}).FulfillReservations(
		context.Background(), &toyohrpb.FulfillReservationsRequest{})
	if err != nil {
		t.Fatalf("FulfillReservations: %v", err)
	}
	if len(resp.GetDocument()) != 0 {
		t.Errorf("document = %v, want none", resp.GetDocument())
	}
	if !resp.GetTimestamp().AsTime().Equal(now) {
		t.Errorf("timestamp = %s, want %s", resp.GetTimestamp().AsTime(), now)
	}
}

// TestGameRecordWithoutADocumentStoreIsUnimplemented: every GameRecord call
// that GRID0+ answers stores a document, so a service built without a
// document store says Unimplemented rather than pretending.
func TestGameRecordWithoutADocumentStoreIsUnimplemented(t *testing.T) {
	svc, _ := newService(t, time.Now())
	g := gameRecordService{Service: svc}

	if _, err := g.InitializeAttributes(context.Background(), &toyohrpb.InitializeAttributesRequest{}); status.Code(err) != codes.Unimplemented {
		t.Errorf("InitializeAttributes = %v, want Unimplemented", err)
	}
	if _, err := g.CreateBankaraChallenge(context.Background(), &toyohrpb.CreateBankaraChallengeRequest{}); status.Code(err) != codes.Unimplemented {
		t.Errorf("CreateBankaraChallenge = %v, want Unimplemented", err)
	}
	if _, err := g.CreateFestPowerMeasurement(context.Background(), &toyohrpb.CreateFestPowerMeasurementRequest{}); status.Code(err) != codes.Unimplemented {
		t.Errorf("CreateFestPowerMeasurement = %v, want Unimplemented", err)
	}
}
