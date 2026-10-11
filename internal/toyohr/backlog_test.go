package toyohr

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/n-popescu/grid0plus/internal/ugcstore"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
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

func TestAnUnissuedCodeIsNotFound(t *testing.T) {
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

// TestASharedScenarioCodeResolvesToTheJob: the code another player types
// in, with dashes and in lower case, hands back the job document that was
// registered. Unimplemented, the console showed an error at the end of
// sharing (2026-10-08).
func TestASharedScenarioCodeResolvesToTheJob(t *testing.T) {
	svc, _ := newService(t, time.Now())
	svc.docs = ugcstore.NewService(ugcstore.Options{
		Store: svc.store, Log: discardLogger(), Tenant: testTenant,
		CallerID: func(context.Context) (string, bool) { return testCaller, true },
		Now:      time.Now,
	})
	ctx := context.Background()
	job := "tenants/current/documents/services/GameRecord/users/" + testCaller + "/CoopResults/job1"
	if _, err := svc.docs.Put(ctx, testCaller, job, &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"wave": {ValueType: &commonpb.Value_IntegerValue{IntegerValue: 3}},
	}}); err != nil {
		t.Fatal(err)
	}
	reg, err := (coopScenarioService{Service: svc}).RegisterDocument(ctx,
		&toyohrpb.CoopScenarioRegisterDocumentRequest{Name: job})
	if err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}
	code := reg.GetCode().GetName()[strings.LastIndex(reg.GetCode().GetName(), "/")+1:]
	if len(code) != 16 || code[0] != 'S' {
		t.Fatalf("code = %q", code)
	}
	typed := strings.ToLower(code[:4] + "-" + code[4:8] + "-" + code[8:12] + "-" + code[12:])
	got, err := (coopScenarioService{Service: svc}).ResolveCoopScenarioCode(ctx,
		&toyohrpb.ResolveCoopScenarioCodeRequest{Name: typed})
	if err != nil {
		t.Fatalf("resolve %q: %v", typed, err)
	}
	if got.GetDocument().GetFields().GetFields()["wave"].GetIntegerValue() != 3 {
		t.Errorf("resolved %v, want the registered job", got.GetDocument())
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
