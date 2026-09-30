package gamesync

import (
	"strings"
	"sync"

	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

// watchers routes document changes to the consoles watching for them.
type watchers struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

// subscriber is one open KeepUserSession stream and what it is watching.
type subscriber struct {
	userID string
	// The referee-result collection is shared by name across matches. A
	// subscriber must only see verdicts for its verified game session.
	sessionID string

	// changes is buffered, and a send that would block is dropped. A console
	// that has stopped reading its stream must not be able to stall a write for
	// every other console in the lobby -- the same trade internal/matchmaking's
	// watchers and internal/presencebus both make.
	changes chan *syncpb.DocumentChange

	mu sync.Mutex
	// documents are the exact document names this subscriber watches, by target.
	documents map[string][]string
	// collections are the collection prefixes it watches, by target.
	collections map[string]string
}

func newWatchers() *watchers {
	return &watchers{subs: map[*subscriber]struct{}{}}
}

func (w *watchers) subscribe(userID, sessionID string) *subscriber {
	sub := &subscriber{
		userID:      userID,
		sessionID:   sessionID,
		changes:     make(chan *syncpb.DocumentChange, 64),
		documents:   map[string][]string{},
		collections: map[string]string{},
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.subs[sub] = struct{}{}
	return sub
}

func (w *watchers) unsubscribe(sub *subscriber) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.subs, sub)
}

// addTarget starts watching a target.
//
// A target is either a list of exact document names or one collection. Both are
// kept under the target's own name, so removing a target removes exactly what it
// added rather than everything that happens to overlap.
func (w *watchers) addTarget(sub *subscriber, target *syncpb.Target) {
	if target == nil || target.GetName() == "" {
		return
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()

	switch t := target.GetTargetType().(type) {
	case *syncpb.Target_Documents:
		sub.documents[target.GetName()] = append([]string(nil), t.Documents.GetDocuments()...)
	case *syncpb.Target_Collection:
		sub.collections[target.GetName()] = t.Collection.GetCollection()
	}
}

func (w *watchers) removeTarget(sub *subscriber, name string) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	delete(sub.documents, name)
	delete(sub.collections, name)
}

// publish delivers changes to every subscriber watching the documents they
// affect.
//
// The target id is stamped per subscriber, because the same document can be
// watched under different target names by different consoles — and the client
// matches a change to the target it asked for.
func (w *watchers) publish(changes []*syncpb.DocumentChange) {
	if len(changes) == 0 {
		return
	}

	w.mu.Lock()
	subs := make([]*subscriber, 0, len(w.subs))
	for sub := range w.subs {
		subs = append(subs, sub)
	}
	w.mu.Unlock()

	for _, sub := range subs {
		for _, change := range changes {
			targetID, ok := sub.targetFor(change.GetDocument().GetName())
			if !ok {
				continue
			}
			// Copied so each subscriber's target id is its own. The Document
			// inside was already cloned once per write, and it is only read
			// from here on.
			out := &syncpb.DocumentChange{
				TargetId:           targetID,
				DocumentChangeType: change.GetDocumentChangeType(),
				Document:           change.GetDocument(),
			}
			select {
			case sub.changes <- out:
			default:
			}
		}
	}
}

// targetFor returns the target a document name matches, if any.
func (s *subscriber) targetFor(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	if strings.HasPrefix(name, refereeResultPrefix) &&
		(s.sessionID == "" || !strings.HasSuffix(name, "_"+s.sessionID)) {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for target, names := range s.documents {
		for _, n := range names {
			if n == name {
				return wireTargetID(target), true
			}
		}
	}
	for target, collection := range s.collections {
		prefix := strings.TrimSuffix(collection, "/") + "/"
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// Direct children only, matching store.list: a collection target that
		// fired for a whole subtree would deliver changes the client has no
		// document for.
		if strings.Contains(strings.TrimPrefix(name, prefix), "/") {
			continue
		}
		return wireTargetID(target), true
	}
	return "", false
}
