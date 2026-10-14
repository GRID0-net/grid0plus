package gamesync

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A deliberately paused transport injects a committed update while an initial
// snapshot is being sent. The live event must follow the complete LISTED
// boundary, never appear between its EXIST documents.
func TestLiveUpdatesCannotSplitAnInitialWatchSnapshot(t *testing.T) {
	h := newHarness(t)
	h.srv.refreshEvery = time.Hour
	peer, err := h.srv.newSession("alice", "userSessions/us-alice")
	if err != nil {
		t.Fatal(err)
	}
	peer.details = &SessionDetails{SessionID: "room"}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), callerKey{}, peer), 3*time.Second)
	defer cancel()
	var names []string
	for i := 0; i < 80; i++ {
		name := fmt.Sprintf("docs/snapshot/%02d", i)
		names = append(names, name)
		h.srv.docsFor(peer).seed(doc(name, map[string]*commonpb.Value{"sequence": intValue(0)}))
	}
	request := &syncpb.KeepUserSessionRequest{RequestType: &syncpb.KeepUserSessionRequest_UpdateTarget{UpdateTarget: &syncpb.UpdateTargetRequest{Target: &syncpb.Target{Name: "targets/9", TargetType: &syncpb.Target_Documents{Documents: &syncpb.DocumentsTarget{Documents: names}}}}}}
	stream := &orderedTestStream{ctx: ctx, request: request, listed: make(chan struct{})}
	stream.onFirstSend = func() error {
		_, err := h.srv.WriteDocuments(ctx, &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{merge(doc(names[0], map[string]*commonpb.Value{"sequence": intValue(1)}))}})
		return err
	}
	done := make(chan error, 1)
	go func() { done <- h.srv.KeepUserSession(stream) }()
	select {
	case <-stream.listed:
	case <-ctx.Done():
		t.Fatal("snapshot never finished")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if stream.interleaved {
		t.Fatal("live UPDATED arrived inside the initial snapshot, before LISTED")
	}
	if stream.documents != len(names) {
		t.Fatalf("snapshot contains %d documents, want %d", stream.documents, len(names))
	}
}

func TestWatchDeliveryNeverRollsBackOnePiaSenderOrDropsAnother(t *testing.T) {
	var wire []*syncpb.KeepUserSessionResponse
	delivery := newWatchDelivery(func(msg *syncpb.KeepUserSessionResponse) error { wire = append(wire, msg); return nil })
	change := func(sender, target string, sequence int64) *syncpb.DocumentChange {
		d := doc(peerInboxPrefix+"us-recipient", map[string]*commonpb.Value{"susid": stringValue(sender), "pl": {ValueType: &commonpb.Value_BytesValue{BytesValue: []byte{byte(sequence), 0, 255}}}})
		d.UpdateTime = timestamppb.New(time.Unix(sequence, 0))
		return &syncpb.DocumentChange{TargetId: target, DocumentChangeType: syncpb.DocumentChange_UPDATED, Document: d}
	}
	newer := change("us-alice", "9", 2)
	before := proto.Clone(newer).(*syncpb.DocumentChange)
	if err := delivery.send(&syncpb.KeepUserSessionResponse{ResponseType: &syncpb.KeepUserSessionResponse_DocumentChange{DocumentChange: newer}}); err != nil {
		t.Fatal(err)
	}
	if !delivery.stale(change("us-alice", "9", 1)) {
		t.Fatal("older queued peer state would overwrite newer snapshot")
	}
	if delivery.stale(change("us-bob", "9", 1)) {
		t.Fatal("another sender's peer state was lost")
	}
	if delivery.stale(change("us-alice", "10", 1)) {
		t.Fatal("another target's state was lost")
	}
	if delivery.stale(newer) {
		t.Fatal("equal-time update or referee refresh was suppressed")
	}
	if !proto.Equal(newer, before) || !proto.Equal(wire[0].GetDocumentChange(), before) {
		t.Fatal("opaque packet or sender metadata was rewritten")
	}
	deleted := change("us-alice", "9", 1)
	deleted.DocumentChangeType = syncpb.DocumentChange_DELETED
	if delivery.stale(deleted) {
		t.Fatal("delete was mistaken for an old update")
	}
	if err := delivery.send(&syncpb.KeepUserSessionResponse{ResponseType: &syncpb.KeepUserSessionResponse_DocumentChange{DocumentChange: deleted}}); err != nil {
		t.Fatal(err)
	}
	if delivery.stale(change("us-alice", "9", 1)) {
		t.Fatal("delete did not clear the old watermark")
	}
	if err := delivery.send(&syncpb.KeepUserSessionResponse{ResponseType: &syncpb.KeepUserSessionResponse_DocumentChange{DocumentChange: newer}}); err != nil {
		t.Fatal(err)
	}
	delivery.reset("9")
	if delivery.stale(change("us-alice", "9", 1)) {
		t.Fatal("reused target kept old watermarks")
	}
}

func TestQueuedChangesDoNotReachADeletedOrRetargetedWatch(t *testing.T) {
	w := newWatchers()
	sub := w.subscribe("alice", "userSessions/us-alice", "room")
	defer w.unsubscribe(sub)
	target := func(name string) *syncpb.Target {
		return &syncpb.Target{Name: "targets/9", TargetType: &syncpb.Target_Documents{Documents: &syncpb.DocumentsTarget{Documents: []string{name}}}}
	}
	w.addTarget(sub, target("docs/old"))
	if !sub.watches("9", "docs/old") {
		t.Fatal("active target missing")
	}
	w.addTarget(sub, target("docs/new"))
	if sub.watches("9", "docs/old") {
		t.Fatal("old queued update still matches retargeted watch")
	}
	w.removeTarget(sub, "targets/9")
	if sub.watches("9", "docs/new") {
		t.Fatal("deleted watch still receives queued updates")
	}
}

type orderedTestStream struct {
	ctx                            context.Context
	request                        *syncpb.KeepUserSessionRequest
	onFirstSend                    func() error
	listed                         chan struct{}
	started, finished, interleaved bool
	documents                      int
}

func (s *orderedTestStream) Context() context.Context     { return s.ctx }
func (s *orderedTestStream) SetHeader(metadata.MD) error  { return nil }
func (s *orderedTestStream) SendHeader(metadata.MD) error { return nil }
func (s *orderedTestStream) SetTrailer(metadata.MD)       {}
func (s *orderedTestStream) SendMsg(any) error            { return nil }
func (s *orderedTestStream) RecvMsg(any) error            { return io.EOF }
func (s *orderedTestStream) Recv() (*syncpb.KeepUserSessionRequest, error) {
	if s.request != nil {
		r := s.request
		s.request = nil
		return r, nil
	}
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}
func (s *orderedTestStream) Send(msg *syncpb.KeepUserSessionResponse) error {
	if !s.started {
		s.started = true
		if err := s.onFirstSend(); err != nil {
			return err
		}
	}
	if d := msg.GetDocumentChange(); d != nil && !s.finished {
		if d.GetDocumentChangeType() != syncpb.DocumentChange_EXIST {
			s.interleaved = true
		} else {
			s.documents++
		}
	}
	if tc := msg.GetTargetChange(); tc != nil && tc.GetTargetChangeType() == syncpb.TargetChange_LISTED && !s.finished {
		s.finished = true
		close(s.listed)
	}
	return nil
}
