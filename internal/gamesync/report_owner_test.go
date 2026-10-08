package gamesync

import (
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestAReportCannotWriteAnotherPlayersResultThroughAnyWritePath(t *testing.T) {
	for _, path := range []string{"ordinary", "lazy", "transaction", "deferment"} {
		t.Run(path, func(t *testing.T) {
			h := newHarness(t)
			caller, err := h.srv.newSession("alice", "userSessions/alice-session")
			if err != nil {
				t.Fatal(err)
			}
			caller.details = &SessionDetails{SessionID: "room"}
			ctx := h.authed(caller.accessToken)
			bad := merge(doc("docs/@@RefereeReport/battle_room/User/bob-session", map[string]*commonpb.Value{"team_result": intValue(1)}))
			switch path {
			case "ordinary":
				_, err = h.client.WriteDocuments(ctx, &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{bad}})
			case "lazy":
				_, err = h.client.LazyWriteDocuments(ctx, &syncpb.LazyWriteDocumentsRequest{WriteOperations: []*syncpb.LazyWriteOperation{{OperationType: &syncpb.LazyWriteOperation_MergeDocument{MergeDocument: bad.GetMergeDocument()}}}})
			case "transaction":
				begin, e := h.client.BeginTransaction(ctx, &syncpb.BeginTransactionRequest{})
				if e != nil {
					t.Fatal(e)
				}
				_, err = h.client.CommitTransaction(ctx, &syncpb.CommitTransactionRequest{Transaction: begin.GetTransaction(), WriteOperations: []*syncpb.WriteOperation{bad}})
				if status.Code(err) != codes.PermissionDenied {
					t.Fatalf("rejection = %v", err)
				}
				_, e = h.client.CommitTransaction(ctx, &syncpb.CommitTransactionRequest{Transaction: begin.GetTransaction(), WriteOperations: []*syncpb.WriteOperation{merge(doc("docs/test", map[string]*commonpb.Value{"ok": intValue(1)}))}})
				if e != nil {
					t.Fatalf("rejected report consumed transaction: %v", e)
				}
			case "deferment":
				_, err = h.client.WriteDocuments(ctx, &syncpb.WriteDocumentsRequest{DefermentOperations: []*syncpb.DefermentOperation{{OperationType: &syncpb.DefermentOperation_UpdateDeferment{UpdateDeferment: &syncpb.UpdateDefermentRequest{Deferment: &syncpb.Deferment{Name: "cleanup", WriteOperations: []*syncpb.WriteOperation{bad}}}}}}})
			}
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("rejection = %v", err)
			}
			if _, e := h.client.GetDocument(ctx, &syncpb.GetDocumentRequest{Name: bad.GetMergeDocument().GetDocument().GetName()}); status.Code(e) != codes.NotFound {
				t.Fatalf("rejected report was stored: %v", e)
			}
		})
	}
}

func TestAWholeRosterReportOnlyPublishesItsAuthenticatedReporter(t *testing.T) {
	h := newHarness(t)
	caller, err := h.srv.newSession("alice", "userSessions/alice-session")
	if err != nil {
		t.Fatal(err)
	}
	caller.details = &SessionDetails{SessionID: "room"}
	ctx := h.authed(caller.accessToken)
	name := "docs/@@RefereeReport/battle_room/User/alice-session"
	personal := &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"alice-session": {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"score": intValue(3)}}}},
		"bob-session":   {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"npln_user_id": stringValueForReport("bob")}}}},
	}}}}
	if _, err := h.client.WriteDocuments(ctx, &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{merge(doc(name, map[string]*commonpb.Value{"personal_result": personal}))}}); err != nil {
		t.Fatal(err)
	}
	stored, err := h.client.GetDocument(ctx, &syncpb.GetDocumentRequest{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	people := stored.GetFields().GetFields()["personal_result"].GetMapValue().GetFields()
	if len(people) != 1 || people["alice-session"].GetMapValue().GetFields()["npln_user_id"].GetStringValue() != "alice" {
		t.Fatalf("stored reporter identities = %v", people)
	}
	personal.GetMapValue().Fields["alice-session"].GetMapValue().Fields["npln_user_id"] = stringValueForReport("bob")
	_, err = h.client.WriteDocuments(ctx, &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{
		merge(doc("docs/atomic-probe", map[string]*commonpb.Value{"written": intValue(1)})),
		merge(doc(name, map[string]*commonpb.Value{"personal_result": personal})),
	}})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("contradictory identity = %v", err)
	}
	if _, err := h.client.GetDocument(ctx, &syncpb.GetDocumentRequest{Name: "docs/atomic-probe"}); status.Code(err) != codes.NotFound {
		t.Fatalf("rejected batch had a partial write: %v", err)
	}
}
