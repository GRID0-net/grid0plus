package gamesync

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/n-popescu/switchnet/internal/gamesyncstore"
	"github.com/n-popescu/switchnet/internal/rpcauth"
	commonpb "github.com/n-popescu/switchnet/proto/common"
	v1 "github.com/n-popescu/switchnet/proto/gamesync/v1"
)

func newTestService() (*Service, func() time.Time) {
	now := func() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) }
	return NewService(gamesyncstore.New(now), nil, now), now
}

func authedContext(userID string) context.Context {
	return rpcauth.With(context.Background(), rpcauth.Caller{UserID: userID, DeviceAccountID: "da-" + userID})
}

func strVal(s string) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: s}}
}

func TestIssueTokenRequiresAuthentication(t *testing.T) {
	svc, _ := newTestService()
	_, err := svc.IssueToken(context.Background(), &v1.IssueTokenRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got %v, want Unauthenticated", err)
	}
}

func TestIssueTokenMintsATokenForTheAuthenticatedCaller(t *testing.T) {
	svc, _ := newTestService()
	resp, err := svc.IssueToken(authedContext("alice"), &v1.IssueTokenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	tok := resp.GetToken()
	if tok.GetUserSession() != "alice" {
		t.Errorf("got user_session %q, want alice", tok.GetUserSession())
	}
	if tok.GetAccessToken() == "" || tok.GetRefreshToken() == "" {
		t.Errorf("got empty token: %+v", tok)
	}
}

func TestIssueTokenIgnoresTheRequestsOwnUserSessionField(t *testing.T) {
	// The console naming a user is not what proves one -- the same
	// distinction nn.npln.auth.v1.Auth.IssueToken's own request makes. A
	// caller authenticated as "alice" cannot mint a token for "mallory" by
	// naming her in the request body.
	svc, _ := newTestService()
	resp, err := svc.IssueToken(authedContext("alice"), &v1.IssueTokenRequest{UserSession: "mallory"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetToken().GetUserSession() != "alice" {
		t.Errorf("got %q, want alice", resp.GetToken().GetUserSession())
	}
}

func TestRefreshTokenExchangesAValidRefreshToken(t *testing.T) {
	svc, _ := newTestService()
	issued, err := svc.IssueToken(authedContext("alice"), &v1.IssueTokenRequest{})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := svc.RefreshToken(authedContext("alice"), &v1.RefreshTokenRequest{RefreshToken: issued.GetToken().GetRefreshToken()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetToken().GetAccessToken() == issued.GetToken().GetAccessToken() {
		t.Error("refresh should have minted a new access token")
	}
}

func TestRefreshTokenRefusesAnotherCallersToken(t *testing.T) {
	svc, _ := newTestService()
	issued, err := svc.IssueToken(authedContext("alice"), &v1.IssueTokenRequest{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.RefreshToken(authedContext("mallory"), &v1.RefreshTokenRequest{RefreshToken: issued.GetToken().GetRefreshToken()})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got %v, want Unauthenticated", err)
	}
}

func TestRefreshTokenConsumesTheOldToken(t *testing.T) {
	svc, _ := newTestService()
	issued, err := svc.IssueToken(authedContext("alice"), &v1.IssueTokenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	req := &v1.RefreshTokenRequest{RefreshToken: issued.GetToken().GetRefreshToken()}
	if _, err := svc.RefreshToken(authedContext("alice"), req); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RefreshToken(authedContext("alice"), req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected the second use of the same refresh token to fail, got %v", err)
	}
}

func TestWriteThenGetDocumentRoundTrips(t *testing.T) {
	svc, _ := newTestService()
	ctx := authedContext("alice")
	name := "tenants/t/games/g1"

	_, err := svc.WriteDocuments(ctx, &v1.WriteDocumentsRequest{WriteOperations: []*v1.WriteOperation{{
		OperationType: &v1.WriteOperation_UpdateDocument{UpdateDocument: &v1.UpdateDocumentRequest{
			Document: &v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"state": strVal("lobby")}}},
		}},
	}}})
	if err != nil {
		t.Fatal(err)
	}

	doc, err := svc.GetDocument(ctx, &v1.GetDocumentRequest{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetFields().GetFields()["state"].GetStringValue() != "lobby" {
		t.Errorf("got %+v", doc.GetFields())
	}
}

func TestGetDocumentOfSomethingMissingIsNotFound(t *testing.T) {
	svc, _ := newTestService()
	_, err := svc.GetDocument(authedContext("alice"), &v1.GetDocumentRequest{Name: "tenants/t/games/nope"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got %v, want NotFound", err)
	}
}

func TestGetDocumentAppliesAReadMask(t *testing.T) {
	svc, _ := newTestService()
	ctx := authedContext("alice")
	name := "tenants/t/games/g1"
	if _, err := svc.WriteDocuments(ctx, &v1.WriteDocumentsRequest{WriteOperations: []*v1.WriteOperation{{
		OperationType: &v1.WriteOperation_MergeDocument{MergeDocument: &v1.MergeDocumentRequest{
			Document: &v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"state": strVal("lobby"), "secret": strVal("dont-send-me"),
			}}},
		}},
	}}}); err != nil {
		t.Fatal(err)
	}

	doc, err := svc.GetDocument(ctx, &v1.GetDocumentRequest{Name: name, ReadMask: &fieldmaskpb.FieldMask{Paths: []string{"state"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.GetFields().GetFields()["secret"]; ok {
		t.Errorf("read mask should have excluded 'secret', got %+v", doc.GetFields())
	}
	if doc.GetFields().GetFields()["state"].GetStringValue() != "lobby" {
		t.Errorf("got %+v", doc.GetFields())
	}
}

func TestReadDocumentsReportsAMissingDocumentRatherThanErroring(t *testing.T) {
	svc, _ := newTestService()
	ctx := authedContext("alice")
	name := "tenants/t/games/g1"
	if _, err := svc.WriteDocuments(ctx, &v1.WriteDocumentsRequest{WriteOperations: []*v1.WriteOperation{{
		OperationType: &v1.WriteOperation_MergeDocument{MergeDocument: &v1.MergeDocumentRequest{Document: &v1.Document{Name: name}}},
	}}}); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.ReadDocuments(ctx, &v1.ReadDocumentsRequest{Documents: []string{name, "tenants/t/games/nope"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetResults()[0].GetFound() == nil {
		t.Errorf("expected the first result to be found: %+v", resp.GetResults()[0])
	}
	if resp.GetResults()[1].GetMissing() != "tenants/t/games/nope" {
		t.Errorf("expected the second result to report the missing name: %+v", resp.GetResults()[1])
	}
}

func TestListDocumentsRequiresAuthentication(t *testing.T) {
	svc, _ := newTestService()
	_, err := svc.ListDocuments(context.Background(), &v1.ListDocumentsRequest{Parent: "tenants/t/games"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got %v, want Unauthenticated", err)
	}
}

func TestWriteDocumentsHonoursAFailedPrecondition(t *testing.T) {
	svc, _ := newTestService()
	ctx := authedContext("alice")
	_, err := svc.WriteDocuments(ctx, &v1.WriteDocumentsRequest{WriteOperations: []*v1.WriteOperation{{
		OperationType: &v1.WriteOperation_UpdateDocument{UpdateDocument: &v1.UpdateDocumentRequest{
			Document:        &v1.Document{Name: "tenants/t/games/g1"},
			CurrentDocument: &v1.Precondition{ConditionType: &v1.Precondition_Exists{Exists: true}},
		}},
	}}})
	if err == nil {
		t.Fatal("expected a precondition failure updating a document that does not exist")
	}
}

func TestBeginCommitTransactionRoundTrips(t *testing.T) {
	svc, _ := newTestService()
	ctx := authedContext("alice")
	name := "tenants/t/games/g1"

	begin, err := svc.BeginTransaction(ctx, &v1.BeginTransactionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CommitTransaction(ctx, &v1.CommitTransactionRequest{
		Transaction: begin.GetTransaction(),
		WriteOperations: []*v1.WriteOperation{{
			OperationType: &v1.WriteOperation_MergeDocument{MergeDocument: &v1.MergeDocumentRequest{
				Document: &v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"state": strVal("started")}}},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	doc, err := svc.GetDocument(ctx, &v1.GetDocumentRequest{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetFields().GetFields()["state"].GetStringValue() != "started" {
		t.Errorf("got %+v", doc.GetFields())
	}
}

func TestRollbackTransactionThenCommitFails(t *testing.T) {
	svc, _ := newTestService()
	ctx := authedContext("alice")
	begin, err := svc.BeginTransaction(ctx, &v1.BeginTransactionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RollbackTransaction(ctx, &v1.RollbackTransactionRequest{Transaction: begin.GetTransaction()}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CommitTransaction(ctx, &v1.CommitTransactionRequest{Transaction: begin.GetTransaction()}); err == nil {
		t.Fatal("expected committing a rolled-back transaction to fail")
	}
}

// TestCreateRoundEchoesTheSubmittedRound covers the one thing this RPC is
// known to be safe doing without a capture: handing back exactly what the
// caller sent, never inventing a value for Unk1-4.
func TestCreateRoundEchoesTheSubmittedRound(t *testing.T) {
	svc, _ := newTestService()
	in := &v1.Round{Unk1: "a", Unk2: "b", Unk3: "c", Unk4: []string{"d", "e"}}
	got, err := svc.CreateRound(authedContext("alice"), &v1.CreateRoundRequest{Round: in})
	if err != nil {
		t.Fatalf("CreateRound: %v", err)
	}
	if got != in {
		t.Fatalf("got %+v, want the exact submitted Round echoed back", got)
	}
}

func TestCreateRoundWithNoRoundGivenReturnsAnEmptyOne(t *testing.T) {
	svc, _ := newTestService()
	got, err := svc.CreateRound(authedContext("alice"), &v1.CreateRoundRequest{})
	if err != nil {
		t.Fatalf("CreateRound: %v", err)
	}
	if got.GetUnk1() != "" || got.GetUnk2() != "" || got.GetUnk3() != "" || len(got.GetUnk4()) != 0 {
		t.Fatalf("got %+v, want an empty Round", got)
	}
}

func TestCreateRoundRequiresAuthentication(t *testing.T) {
	svc, _ := newTestService()
	_, err := svc.CreateRound(context.Background(), &v1.CreateRoundRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got %v, want Unauthenticated", err)
	}
}

func TestLazyWriteDocumentsAppliesMergeOperations(t *testing.T) {
	svc, _ := newTestService()
	ctx := authedContext("alice")
	name := "tenants/t/games/g1"
	_, err := svc.LazyWriteDocuments(ctx, &v1.LazyWriteDocumentsRequest{
		WriteOperations: []*v1.LazyWriteOperation{{
			OperationType: &v1.LazyWriteOperation_MergeDocument{MergeDocument: &v1.MergeDocumentRequest{
				Document: &v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"state": strVal("lobby")}}},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := svc.GetDocument(ctx, &v1.GetDocumentRequest{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetFields().GetFields()["state"].GetStringValue() != "lobby" {
		t.Errorf("got %+v", doc.GetFields())
	}
}
