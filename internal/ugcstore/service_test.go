package ugcstore

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	ugcpb "github.com/n-popescu/grid0plus/proto/ugcstore/v1"
)

// runQueryStream is a minimal ServerStreamingServer that collects what is sent.
type runQueryStream struct {
	ctx  context.Context
	sent []*ugcpb.RunQueryResponse
}

func (r *runQueryStream) Send(m *ugcpb.RunQueryResponse) error {
	r.sent = append(r.sent, m)
	return nil
}
func (r *runQueryStream) Context() context.Context     { return r.ctx }
func (r *runQueryStream) SetHeader(metadata.MD) error  { return nil }
func (r *runQueryStream) SendHeader(metadata.MD) error { return nil }
func (r *runQueryStream) SetTrailer(metadata.MD)       {}
func (r *runQueryStream) SendMsg(any) error            { return nil }
func (r *runQueryStream) RecvMsg(any) error            { return nil }

func newServiceForTest(t *testing.T, uid string) *Service {
	t.Helper()
	return NewService(Options{
		Store:  db.NewMemory(),
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tenant: "dce9377b",
		CallerID: func(context.Context) (string, bool) {
			return uid, uid != ""
		},
		Now: func() time.Time { return time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC) },
	})
}

// TestRunQueryAnswersZeroMatchesRatherThanUnimplemented is the regression
// test for a real crash: RunQuery left Unimplemented (grpc-status 12) is the last
// RPC in the boot sequence still unanswered, and shows as a communication error
// right after entering the lobby. A real server answers a query with no matches
// as exactly one streamed message carrying only read_time.
func TestRunQueryAnswersZeroMatchesRatherThanUnimplemented(t *testing.T) {
	svc := newServiceForTest(t, "player-1")
	stream := &runQueryStream{ctx: context.Background()}

	err := svc.RunQuery(&ugcpb.RunQueryRequest{
		Parent: "tenants/current/documents/users/player-1",
	}, stream)
	if err != nil {
		t.Fatalf("RunQuery: %v -- this is what a real console reads as a communication error", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("sent %d messages, want exactly 1", len(stream.sent))
	}
	got := stream.sent[0]
	if got.GetReadTime() == nil {
		t.Error("read_time is unset")
	}
	if got.GetDocument() != nil {
		t.Error("document is set, want unset -- this server has no matching document to answer with")
	}
}

func TestRunQueryRefusesAnUnauthenticatedCaller(t *testing.T) {
	svc := newServiceForTest(t, "")
	stream := &runQueryStream{ctx: context.Background()}

	err := svc.RunQuery(&ugcpb.RunQueryRequest{}, stream)
	if err == nil {
		t.Fatal("RunQuery succeeded with no authenticated caller")
	}
}

// TestRemovingAnAbsentAttachmentDoesNotLoseTheBatch is Side Order's stats
// write: an UnsetAttachment beside an ordinary update. Refused, the console's
// whole batch was dropped and its stats never stored.
func TestRemovingAnAbsentAttachmentDoesNotLoseTheBatch(t *testing.T) {
	const uid = "26baf7658281f2b1"
	s := newServiceForTest(t, uid)
	name := "tenants/current/documents/users/" + uid + "/sideOrder/stats"
	_, err := s.CommitDocuments(context.Background(), &ugcpb.CommitDocumentsRequest{
		WriteOperations: []*ugcpb.WriteOperation{
			{OperationType: &ugcpb.WriteOperation_UnsetAttachment{
				UnsetAttachment: &ugcpb.UnsetAttachmentRequest{Parent: name},
			}},
			{OperationType: &ugcpb.WriteOperation_UpdateDocument{
				UpdateDocument: &ugcpb.UpdateDocumentRequest{Document: &ugcpb.Document{
					Name:   name,
					Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"floor": {ValueType: &commonpb.Value_IntegerValue{IntegerValue: 3}}}},
				}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("commit with an UnsetAttachment: %v", err)
	}
	doc, err := s.GetDocument(context.Background(), &ugcpb.GetDocumentRequest{Name: name})
	if err != nil {
		t.Fatalf("the update in the same batch was not stored: %v", err)
	}
	if doc.GetFields().GetFields()["floor"].GetIntegerValue() != 3 {
		t.Errorf("stored fields = %v", doc.GetFields())
	}
}
