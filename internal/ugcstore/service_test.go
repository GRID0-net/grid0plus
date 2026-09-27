package ugcstore

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/n-popescu/grid0plus/internal/db"
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
// test for a real crash: RunQuery left Unimplemented (grpc-status 12) was
// the last RPC in the confirmed boot sequence still unanswered, and
// reproduced on real hardware on 2026-09-24 as a communication error right
// after entering the lobby. A real capture (facts only, no code taken --
// see RunQuery's own doc comment) shows a real server answers a query with
// no matches as exactly one streamed message carrying only read_time.
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
