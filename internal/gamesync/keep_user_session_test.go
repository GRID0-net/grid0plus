package gamesync

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	v1 "github.com/n-popescu/switchnet/proto/gamesync/v1"
)

// fakeKeepUserSessionStream is the minimal bidi grpc.ServerStream fake
// KeepUserSession needs: a cancellable context, a channel of requests to
// Recv, and a channel of what was Sent.
type fakeKeepUserSessionStream struct {
	ctx  context.Context
	in   chan *v1.KeepUserSessionRequest
	sent chan *v1.KeepUserSessionResponse
}

func newFakeKeepUserSessionStream(ctx context.Context) *fakeKeepUserSessionStream {
	return &fakeKeepUserSessionStream{
		ctx:  ctx,
		in:   make(chan *v1.KeepUserSessionRequest, 16),
		sent: make(chan *v1.KeepUserSessionResponse, 16),
	}
}

func (f *fakeKeepUserSessionStream) Send(m *v1.KeepUserSessionResponse) error {
	f.sent <- m
	return nil
}

func (f *fakeKeepUserSessionStream) Recv() (*v1.KeepUserSessionRequest, error) {
	select {
	case req, ok := <-f.in:
		if !ok {
			return nil, errStreamClosed
		}
		return req, nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}

func (f *fakeKeepUserSessionStream) Context() context.Context     { return f.ctx }
func (f *fakeKeepUserSessionStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeKeepUserSessionStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeKeepUserSessionStream) SetTrailer(metadata.MD)       {}
func (f *fakeKeepUserSessionStream) SendMsg(m any) error          { return nil }
func (f *fakeKeepUserSessionStream) RecvMsg(m any) error          { return nil }

type streamClosedError struct{}

func (streamClosedError) Error() string { return "io: EOF" }

var errStreamClosed = streamClosedError{}

func recvChange(t *testing.T, sent chan *v1.KeepUserSessionResponse) *v1.KeepUserSessionResponse {
	t.Helper()
	select {
	case r := <-sent:
		return r
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a response")
		return nil
	}
}

func TestKeepUserSessionEchoesBackWhatWasSent(t *testing.T) {
	svc, _ := newTestService()
	ctx, cancel := context.WithCancel(authedContext("alice"))
	defer cancel()

	stream := newFakeKeepUserSessionStream(ctx)
	done := make(chan error, 1)
	go func() { done <- svc.KeepUserSession(stream) }()

	stream.in <- &v1.KeepUserSessionRequest{RequestType: &v1.KeepUserSessionRequest_Echo{Echo: "ping"}}
	resp := recvChange(t, stream.sent)
	if resp.GetEcho() != "ping" {
		t.Errorf("got %+v", resp)
	}
}

func TestKeepUserSessionRequiresAuthentication(t *testing.T) {
	svc, _ := newTestService()
	stream := newFakeKeepUserSessionStream(context.Background())
	if err := svc.KeepUserSession(stream); err == nil {
		t.Fatal("expected an unauthenticated stream to be refused")
	}
}

func TestKeepUserSessionSendsExistThenListedForACurrentDocumentsTarget(t *testing.T) {
	svc, _ := newTestService()
	ctx := authedContext("alice")
	name := "tenants/t/games/g1"
	if _, err := svc.WriteDocuments(ctx, &v1.WriteDocumentsRequest{WriteOperations: []*v1.WriteOperation{{
		OperationType: &v1.WriteOperation_MergeDocument{MergeDocument: &v1.MergeDocumentRequest{
			Document: &v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"state": strVal("lobby")}}},
		}},
	}}}); err != nil {
		t.Fatal(err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := newFakeKeepUserSessionStream(streamCtx)
	go func() { _ = svc.KeepUserSession(stream) }()

	stream.in <- &v1.KeepUserSessionRequest{
		Name:        "watch-1",
		RequestType: &v1.KeepUserSessionRequest_UpdateTarget{UpdateTarget: &v1.UpdateTargetRequest{Target: &v1.Target{Name: "t1", TargetType: &v1.Target_Documents{Documents: &v1.DocumentsTarget{Documents: []string{name}}}}}},
	}

	exist := recvChange(t, stream.sent)
	dc := exist.GetDocumentChange()
	if dc.GetDocumentChangeType() != v1.DocumentChange_EXIST || dc.GetTargetId() != "t1" {
		t.Fatalf("got %+v", exist)
	}

	listed := recvChange(t, stream.sent)
	tc := listed.GetTargetChange()
	if tc.GetTargetChangeType() != v1.TargetChange_LISTED || tc.GetTargetId() != "t1" {
		t.Fatalf("got %+v", listed)
	}
}

func TestKeepUserSessionPushesALiveChangeToAMatchingTarget(t *testing.T) {
	svc, _ := newTestService()
	ctx := authedContext("alice")
	name := "tenants/t/games/g1"

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := newFakeKeepUserSessionStream(streamCtx)
	go func() { _ = svc.KeepUserSession(stream) }()

	stream.in <- &v1.KeepUserSessionRequest{
		RequestType: &v1.KeepUserSessionRequest_UpdateTarget{UpdateTarget: &v1.UpdateTargetRequest{Target: &v1.Target{Name: "t1", TargetType: &v1.Target_Documents{Documents: &v1.DocumentsTarget{Documents: []string{name}}}}}},
	}
	// Only a TargetChange{LISTED} arrives immediately, since the document
	// does not exist yet.
	listed := recvChange(t, stream.sent)
	if listed.GetTargetChange().GetTargetChangeType() != v1.TargetChange_LISTED {
		t.Fatalf("got %+v", listed)
	}

	if _, err := svc.WriteDocuments(ctx, &v1.WriteDocumentsRequest{WriteOperations: []*v1.WriteOperation{{
		OperationType: &v1.WriteOperation_MergeDocument{MergeDocument: &v1.MergeDocumentRequest{
			Document: &v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"state": strVal("lobby")}}},
		}},
	}}}); err != nil {
		t.Fatal(err)
	}

	pushed := recvChange(t, stream.sent)
	dc := pushed.GetDocumentChange()
	if dc.GetDocumentChangeType() != v1.DocumentChange_UPDATED || dc.GetTargetId() != "t1" || dc.GetDocument().GetName() != name {
		t.Fatalf("got %+v", pushed)
	}
}

func TestKeepUserSessionDeleteTargetStopsFurtherDelivery(t *testing.T) {
	svc, _ := newTestService()
	ctx := authedContext("alice")
	name := "tenants/t/games/g1"

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := newFakeKeepUserSessionStream(streamCtx)
	go func() { _ = svc.KeepUserSession(stream) }()

	stream.in <- &v1.KeepUserSessionRequest{
		RequestType: &v1.KeepUserSessionRequest_UpdateTarget{UpdateTarget: &v1.UpdateTargetRequest{Target: &v1.Target{Name: "t1", TargetType: &v1.Target_Documents{Documents: &v1.DocumentsTarget{Documents: []string{name}}}}}},
	}
	recvChange(t, stream.sent) // LISTED

	stream.in <- &v1.KeepUserSessionRequest{RequestType: &v1.KeepUserSessionRequest_DeleteTarget{DeleteTarget: &v1.DeleteTargetRequest{Name: "t1"}}}
	deleted := recvChange(t, stream.sent)
	if deleted.GetTargetChange().GetTargetChangeType() != v1.TargetChange_DELETED {
		t.Fatalf("got %+v", deleted)
	}

	if _, err := svc.WriteDocuments(ctx, &v1.WriteDocumentsRequest{WriteOperations: []*v1.WriteOperation{{
		OperationType: &v1.WriteOperation_MergeDocument{MergeDocument: &v1.MergeDocumentRequest{Document: &v1.Document{Name: name}}},
	}}}); err != nil {
		t.Fatal(err)
	}

	select {
	case r := <-stream.sent:
		t.Fatalf("expected no further delivery after DeleteTarget, got %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestKeepUserSessionReturnsOnContextCancellation(t *testing.T) {
	svc, _ := newTestService()
	ctx, cancel := context.WithCancel(authedContext("alice"))
	stream := newFakeKeepUserSessionStream(ctx)

	done := make(chan error, 1)
	go func() { done <- svc.KeepUserSession(stream) }()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected a clean return on cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("KeepUserSession did not return after its context was cancelled")
	}
}
