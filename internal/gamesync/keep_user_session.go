package gamesync

import (
	"io"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/n-popescu/switchnet/internal/gamesyncstore"
	"github.com/n-popescu/switchnet/internal/rpcauth"
	v1 "github.com/n-popescu/switchnet/proto/gamesync/v1"
)

// KeepUserSession is the console's live watch connection: it registers and
// removes Target subscriptions and receives DocumentChange/TargetChange
// pushes for them, for as long as the stream stays open.
//
// There is no server-originated keep-alive here, unlike
// internal/toyohr/lobby_messaging.go's RecvMessage. That RPC is
// server-streaming only, so a silent console has no way to know the stream
// is still alive; KeepUserSessionRequest's own Echo field is the console
// driving that instead -- Echo is answered in kind, and nothing is sent
// unprompted. Inventing a proactive ping on top of that would be guessing at
// a keep-alive behaviour nothing here documents, for a stream whose own
// protocol already has one.
func (s *Service) KeepUserSession(stream v1.Gamesync_KeepUserSessionServer) error {
	ctx := stream.Context()
	if _, ok := rpcauth.From(ctx); !ok {
		return status.Error(codes.Unauthenticated, "")
	}

	changes, cancel := s.store.Subscribe()
	defer cancel()

	// Requests are read on their own goroutine so this method's single select
	// loop can also watch the store's change feed -- gRPC only allows one
	// goroutine to call stream.Send, which the loop below is the only one to
	// do, but Recv needs to run concurrently with waiting on changes.
	reqs := make(chan *v1.KeepUserSessionRequest)
	recvErr := make(chan error, 1)
	go func() {
		for {
			req, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			select {
			case reqs <- req:
			case <-ctx.Done():
				return
			}
		}
	}()

	// targets is owned by this loop alone -- the goroutine above only decodes
	// requests onto reqs, it never touches targets or calls Send.
	targets := map[string]*v1.Target{}

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-recvErr:
			if err == io.EOF {
				return nil
			}
			return err
		case req := <-reqs:
			if err := s.handleKeepUserSessionRequest(stream, targets, req); err != nil {
				return err
			}
		case c := <-changes:
			if err := deliverChange(stream, targets, c); err != nil {
				return err
			}
		}
	}
}

func (s *Service) handleKeepUserSessionRequest(
	stream v1.Gamesync_KeepUserSessionServer, targets map[string]*v1.Target, req *v1.KeepUserSessionRequest,
) error {
	switch t := req.GetRequestType().(type) {
	case *v1.KeepUserSessionRequest_Echo:
		return stream.Send(&v1.KeepUserSessionResponse{ResponseType: &v1.KeepUserSessionResponse_Echo{Echo: t.Echo}})

	case *v1.KeepUserSessionRequest_UpdateTarget:
		target := t.UpdateTarget.GetTarget()
		if target.GetName() == "" {
			return status.Error(codes.InvalidArgument, "target has no name")
		}
		targets[target.GetName()] = target

		// Send the current contents of the target first, as EXIST, then
		// LISTED to say the initial listing is done -- the point at which a
		// client can trust that anything not yet seen does not exist, the
		// same "current" milestone Firestore's own Listen RPC has.
		for _, doc := range s.matchingDocuments(target) {
			if err := stream.Send(&v1.KeepUserSessionResponse{ResponseType: &v1.KeepUserSessionResponse_DocumentChange{DocumentChange: &v1.DocumentChange{
				TargetId: target.GetName(), DocumentChangeType: v1.DocumentChange_EXIST, Document: doc,
			}}}); err != nil {
				return err
			}
		}
		return stream.Send(&v1.KeepUserSessionResponse{ResponseType: &v1.KeepUserSessionResponse_TargetChange{TargetChange: &v1.TargetChange{
			TargetId: target.GetName(), TargetChangeType: v1.TargetChange_LISTED,
		}}})

	case *v1.KeepUserSessionRequest_DeleteTarget:
		name := t.DeleteTarget.GetName()
		delete(targets, name)
		return stream.Send(&v1.KeepUserSessionResponse{ResponseType: &v1.KeepUserSessionResponse_TargetChange{TargetChange: &v1.TargetChange{
			TargetId: name, TargetChangeType: v1.TargetChange_DELETED,
		}}})

	default:
		return status.Error(codes.InvalidArgument, "no request given")
	}
}

// matchingDocuments returns every document target currently selects, for the
// EXIST batch UpdateTarget sends when a target is (re-)registered.
func (s *Service) matchingDocuments(target *v1.Target) []*v1.Document {
	switch t := target.GetTargetType().(type) {
	case *v1.Target_Documents:
		var docs []*v1.Document
		for _, name := range t.Documents.GetDocuments() {
			if doc := s.store.Get(name); doc != nil {
				docs = append(docs, doc)
			}
		}
		return docs
	case *v1.Target_Collection:
		docs, _ := s.store.List(t.Collection.GetCollection(), 0, "")
		return docs
	default:
		return nil
	}
}

// deliverChange sends c to every registered target it matches.
func deliverChange(stream v1.Gamesync_KeepUserSessionServer, targets map[string]*v1.Target, c gamesyncstore.Change) error {
	changeType := v1.DocumentChange_UPDATED
	if c.Type == gamesyncstore.Deleted {
		changeType = v1.DocumentChange_DELETED
	}
	for targetID, target := range targets {
		if !targetMatches(target, c.Name) {
			continue
		}
		if err := stream.Send(&v1.KeepUserSessionResponse{ResponseType: &v1.KeepUserSessionResponse_DocumentChange{DocumentChange: &v1.DocumentChange{
			TargetId: targetID, DocumentChangeType: changeType, Document: c.Document,
		}}}); err != nil {
			return err
		}
	}
	return nil
}

func targetMatches(target *v1.Target, name string) bool {
	switch t := target.GetTargetType().(type) {
	case *v1.Target_Documents:
		for _, n := range t.Documents.GetDocuments() {
			if n == name {
				return true
			}
		}
		return false
	case *v1.Target_Collection:
		return parentOf(name) == t.Collection.GetCollection()
	default:
		return false
	}
}

func parentOf(name string) string {
	i := strings.LastIndex(name, "/")
	if i < 0 {
		return ""
	}
	return name[:i]
}
