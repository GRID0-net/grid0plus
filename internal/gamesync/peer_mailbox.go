package gamesync

import (
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

const peerInboxPrefix = "docs/__pgn/All/__stu/"

// Several peers publish through the same recipient document. A single store
// value cannot replay all their connection state when a watch attaches late.
// Keep the latest committed state per authenticated sender, not every packet.
type peerMailbox struct {
	mu      sync.Mutex
	rooms   map[string]map[string]map[string]*syncpb.Document
	expires map[string]time.Time
}

func (s *Server) rememberPeerStates(caller *session, changes []*syncpb.DocumentChange) {
	scope := scopeOf(caller)
	if scope == "" {
		return
	}
	s.mailbox.mu.Lock()
	defer s.mailbox.mu.Unlock()
	if s.mailbox.expires == nil {
		s.mailbox.expires = map[string]time.Time{}
	}
	// Stream loss is not proof that a peer left. Retain the replay state until
	// the authenticated room session expires, including across watch reconnects.
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	for room, expiry := range s.mailbox.expires {
		if !now.Before(expiry) {
			delete(s.mailbox.rooms, room)
			delete(s.mailbox.expires, room)
		}
	}
	if caller.expiresAt.After(s.mailbox.expires[scope]) {
		s.mailbox.expires[scope] = caller.expiresAt
	}
	for _, change := range changes {
		if change.GetDocumentChangeType() == syncpb.DocumentChange_DELETED {
			delete(s.mailbox.rooms[scope], change.GetDocument().GetName())
			continue
		}
		doc := change.GetDocument()
		if !strings.HasPrefix(doc.GetName(), peerInboxPrefix) {
			continue
		}
		fields := doc.GetFields().GetFields()
		// A client-supplied sender must agree with the token. Do not retain a
		// forged identity as another peer's state, or infer identity on a miss.
		if fields["susid"].GetStringValue() != sessionID(caller.userSession) ||
			fields["suid"].GetStringValue() != caller.userID || len(fields["pl"].GetBytesValue()) == 0 {
			continue
		}
		if s.mailbox.rooms == nil {
			s.mailbox.rooms = map[string]map[string]map[string]*syncpb.Document{}
		}
		if s.mailbox.rooms[scope] == nil {
			s.mailbox.rooms[scope] = map[string]map[string]*syncpb.Document{}
		}
		if s.mailbox.rooms[scope][doc.GetName()] == nil {
			s.mailbox.rooms[scope][doc.GetName()] = map[string]*syncpb.Document{}
		}
		// Post-commit hooks can run in a different order from the commits.
		// A late hook must not roll the same sender's replay state backward.
		previous := s.mailbox.rooms[scope][doc.GetName()][sessionID(caller.userSession)]
		if previous != nil && previous.GetUpdateTime().AsTime().After(doc.GetUpdateTime().AsTime()) {
			continue
		}
		s.mailbox.rooms[scope][doc.GetName()][sessionID(caller.userSession)] = proto.Clone(doc).(*syncpb.Document)
	}
}

func (s *Server) peerInboxSnapshot(caller *session, name string) []*syncpb.Document {
	// Only the recipient watches this inbox; never remap outgoing packets
	// from a different inbox back onto the caller's own name.
	if name != peerInboxPrefix+sessionID(caller.userSession) {
		return nil
	}
	s.mailbox.mu.Lock()
	defer s.mailbox.mu.Unlock()
	peers := s.mailbox.rooms[scopeOf(caller)][name]
	senders := make([]string, 0, len(peers))
	for sender := range peers {
		senders = append(senders, sender)
	}
	sort.Strings(senders)
	out := make([]*syncpb.Document, 0, len(senders))
	for _, sender := range senders {
		out = append(out, proto.Clone(peers[sender]).(*syncpb.Document))
	}
	return out
}
