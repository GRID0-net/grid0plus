package gamesync

import (
	"sort"
	"strings"
	"sync"

	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

// watchers routes document changes to the consoles watching for them.
type watchers struct {
	mu      sync.Mutex
	subs    map[string]map[*subscriber]struct{}
	startup func(*syncpb.Document, string, string) *syncpb.Document
}

// subscriber is one open KeepUserSession stream and what it is watching.
type subscriber struct {
	userID      string
	userSession string
	// The referee-result collection is shared by name across matches. A
	// subscriber must only see verdicts for its verified game session.
	sessionID string

	// A slow subscriber must not stall writers in the room. Overflow ends its
	// stream explicitly so lost updates cannot look like a successful delivery.
	changes  chan *syncpb.DocumentChange
	overflow chan struct{}

	mu sync.Mutex
	// documents are the exact document names this subscriber watches, by target.
	documents map[string][]string
	// collections are the collection prefixes it watches, by target.
	collections map[string]string
}

func newWatchers() *watchers {
	return &watchers{subs: map[string]map[*subscriber]struct{}{}}
}

func (w *watchers) subscribe(userID, userSession, sessionID string) *subscriber {
	sub := &subscriber{
		userID:      userID,
		userSession: userSession,
		sessionID:   sessionID,
		changes:     make(chan *syncpb.DocumentChange, 64),
		overflow:    make(chan struct{}, 1),
		documents:   map[string][]string{},
		collections: map[string]string{},
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.subs[sessionID] == nil {
		w.subs[sessionID] = map[*subscriber]struct{}{}
	}
	w.subs[sessionID][sub] = struct{}{}
	return sub
}

func (w *watchers) unsubscribe(sub *subscriber) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.subs[sub.sessionID], sub)
	if len(w.subs[sub.sessionID]) == 0 {
		delete(w.subs, sub.sessionID)
	}
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

// playersIn counts the distinct players with a stream open in a game session.
func (w *watchers) playersIn(scope string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	seen := map[string]bool{}
	for sub := range w.subs[scope] {
		seen[sub.userID] = true
	}
	return len(seen)
}

func (w *watchers) userSessionsIn(scope string) map[string]bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]bool{}
	for sub := range w.subs[scope] {
		out[sessionID(sub.userSession)] = true
	}
	return out
}

// publish delivers changes to every subscriber watching the documents they
// affect.
//
// The target id is stamped per subscriber, because the same document can be
// watched under different target names by different consoles — and the client
// matches a change to the target it asked for.
// Only subscribers in the same game session see a change: the stores are
// per session, and two rooms use the same document names.
func (w *watchers) publish(scope string, changes []*syncpb.DocumentChange) {
	if len(changes) == 0 {
		return
	}

	w.mu.Lock()
	subs := make([]*subscriber, 0, len(w.subs[scope]))
	for sub := range w.subs[scope] {
		subs = append(subs, sub)
	}
	w.mu.Unlock()

	for _, sub := range subs {
		for _, change := range changes {
			ids := sub.targetIDsFor(change.GetDocument().GetName())
			if len(ids) == 0 {
				continue
			}
			visible := change.GetDocument()
			if w.startup != nil {
				visible = w.startup(visible, scope, sub.userSession)
			}
			for _, targetID := range ids {
				// The same collection may be watched under two targets. Each
				// target must get its own boundary for the client to apply it.
				out := &syncpb.DocumentChange{
					TargetId:           targetID,
					DocumentChangeType: change.GetDocumentChangeType(),
					Document:           recipientVerdict(visible, sub.userID),
				}
				select {
				case sub.changes <- out:
				default:
					// Lost Pia state cannot be repaired by replaying the latest
					// single recipient document. Force a fresh watch snapshot.
					select {
					case sub.overflow <- struct{}{}:
					default:
					}
				}
			}
		}
	}
}

// targetIDsFor returns every target a document name matches. One client can
// watch the same collection under multiple target names at once.
func (s *subscriber) targetIDsFor(name string) []string {
	if name == "" {
		return nil
	}
	if strings.HasPrefix(name, refereeResultPrefix) &&
		(s.sessionID == "" || !strings.HasSuffix(name, "_"+s.sessionID)) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string

	for target, names := range s.documents {
		for _, n := range names {
			if n == name {
				ids = append(ids, wireTargetID(target))
				break
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
		ids = append(ids, wireTargetID(target))
	}
	sort.Strings(ids)
	return ids
}

func (s *subscriber) watches(targetID, name string) bool {
	for _, id := range s.targetIDsFor(name) {
		if id == targetID {
			return true
		}
	}
	return false
}
