package gamesync

import (
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

func sessionID(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func wireTargetID(name string) string { return sessionID(name) }

// The session sequence belongs to the player's place in the authenticated
// matchmaking roster. Reusing 1 for every room member makes two distinct
// players appear to have the same connection sequence in Gamesync documents.
func participantRank(caller *session) int64 {
	if caller.details != nil {
		for i, userID := range caller.details.Players {
			if userID == caller.userID {
				return int64(i + 1)
			}
		}
	}
	return 1
}

func (s *Server) collectionSnapshot(caller *session, collection string) []*syncpb.Document {
	docs := s.docs.list(collection)
	visible := docs[:0]
	for _, doc := range docs {
		if normalized, ok := s.watchDocument(caller, doc.GetName()); ok {
			visible = append(visible, normalized)
		}
	}
	docs = visible
	if collection == "docs/@@RefereeResult" {
		if caller.details == nil || caller.details.SessionID == "" {
			docs = nil
		} else {
			visible := docs[:0]
			for _, doc := range docs {
				if strings.HasSuffix(doc.GetName(), "_"+caller.details.SessionID) {
					visible = append(visible, doc)
				}
			}
			docs = visible
		}
	}
	seen := make(map[string]bool, len(docs))
	for _, doc := range docs {
		seen[doc.GetName()] = true
	}
	for _, doc := range s.roomCollectionDocuments(caller, collection) {
		if !seen[doc.GetName()] {
			docs = append(docs, doc)
			seen[doc.GetName()] = true
		}
	}
	if own := s.syntheticCollectionMember(caller, collection); own != nil && !seen[own.GetName()] {
		docs = append(docs, own)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].GetName() < docs[j].GetName() })
	return docs
}

func (s *Server) syntheticCollectionMember(caller *session, collection string) *syncpb.Document {
	var name string
	switch collection {
	case "docs/__us", "docs/__pgn/All/__pus":
		name = collection + "/" + sessionID(caller.userSession)
	}
	if name == "" {
		return nil
	}
	doc, ok := s.watchDocument(caller, name)
	if !ok {
		return nil
	}
	return doc
}

// watchDocument only synthesizes the caller's own UserSession. A missing
// document for another session is not evidence that the caller owns it.
func (s *Server) watchDocument(caller *session, name string) (*syncpb.Document, bool) {
	if strings.HasPrefix(name, refereeResultPrefix) &&
		(caller.details == nil || caller.details.SessionID == "" ||
			!strings.HasSuffix(name, "_"+caller.details.SessionID)) {
		return nil, false
	}
	peer := s.roomPeerForDocument(caller, name)
	if isPlayerSessionDocument(name) && peer == nil {
		return nil, false
	}
	if doc, ok := s.docs.get(name); ok {
		if peer != nil || (name == "docs/MasterCollection/Session" && caller.details != nil) {
			var defaults *syncpb.Document
			if name == "docs/MasterCollection/Session" {
				defaults, _ = s.roomDocument(caller, name)
			} else {
				defaults, _ = s.syntheticSessionDocument(peer, name)
			}
			merged := proto.Clone(doc).(*syncpb.Document)
			if merged.Fields == nil {
				merged.Fields = &commonpb.MapValue{Fields: map[string]*commonpb.Value{}}
			}
			for key, value := range defaults.GetFields().GetFields() {
				if _, present := merged.Fields.Fields[key]; !present {
					merged.Fields.Fields[key] = proto.Clone(value).(*commonpb.Value)
				}
			}
			if peer != nil && (strings.HasPrefix(name, "docs/__us/") || strings.HasPrefix(name, "docs/__pgn/All/__pus/")) {
				// The stored fields came from a client write. Identity comes from
				// the authenticated session, for both this player and room peers.
				merged.Fields.Fields["uid"] = &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: peer.userID}}
				if peer.details != nil && peer.details.SessionID != "" {
					rank := participantRank(peer)
					for _, key := range []string{"ussid", "ucsid", "upcsid"} {
						merged.Fields.Fields[key] = &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: rank}}
					}
				}
			}
			return merged, true
		}
		return doc, true
	}
	if doc, ok := s.roomDocument(caller, name); ok {
		return doc, true
	}
	if peer != nil {
		return s.syntheticSessionDocument(peer, name)
	}
	return s.syntheticSessionDocument(caller, name)
}

func isPlayerSessionDocument(name string) bool {
	return strings.HasPrefix(name, "docs/__us/") ||
		strings.HasPrefix(name, "docs/__pgn/All/__pus/") ||
		strings.HasPrefix(name, "docs/__pgn/All/__stu/")
}

// A room watch includes the other participants' session documents. Their
// client writes can be partial; resolve the owner through the signed Gamesync
// session before supplying the same typed defaults as for the caller's own.
func (s *Server) roomPeerForDocument(caller *session, name string) *session {
	if !isPlayerSessionDocument(name) {
		return nil
	}
	id := sessionID(name)
	if id == sessionID(caller.userSession) {
		return caller
	}
	if caller.details == nil || caller.details.SessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, peer := range s.sessions {
		if peer.details != nil && peer.details.SessionID == caller.details.SessionID &&
			sessionID(peer.userSession) == id && s.now().Before(peer.expiresAt) {
			return peer
		}
	}
	return nil
}

// The client asks target 9 for its own __stu document, but the game uses that
// same target to learn every room peer's P2P state. Only relay a peer's real
// console-written pl blob; a synthetic empty blob cannot identify a peer.
func (s *Server) watchedDocuments(caller *session, names []string) []*syncpb.Document {
	var docs []*syncpb.Document
	seen := map[string]bool{}
	add := func(name string) {
		if seen[name] {
			return
		}
		if doc, ok := s.watchDocument(caller, name); ok {
			docs = append(docs, doc)
			seen[name] = true
		}
	}
	for _, name := range names {
		add(name)
		if name != "docs/__pgn/All/__stu/"+sessionID(caller.userSession) ||
			caller.details == nil || caller.details.SessionID == "" {
			continue
		}
		var peers []string
		s.mu.Lock()
		for _, peer := range s.sessions {
			if peer.details != nil && peer.details.SessionID == caller.details.SessionID &&
				peer.userSession != caller.userSession && s.now().Before(peer.expiresAt) {
				peers = append(peers, "docs/__pgn/All/__stu/"+sessionID(peer.userSession))
			}
		}
		s.mu.Unlock()
		sort.Strings(peers)
		for _, peerName := range peers {
			stored, ok := s.docs.get(peerName)
			if !ok || len(stored.GetFields().GetFields()["pl"].GetBytesValue()) == 0 {
				continue
			}
			// Target 9 names only the recipient's document. A peer-named
			// document on that target is not part of the watch the console
			// requested; peer identity belongs in the mailbox fields instead.
			if peer := s.roomPeerForDocument(caller, peerName); peer != nil {
				docs = append(docs, s.peerMailboxDocument(name, peer, stored))
			}
		}
	}
	return docs
}

// Pia reads peer payloads from the local player's watched __stu document.
// The document name remains the recipient's; the fields identify the peer.
// A peer-named update alone reaches the stream but is not the mailbox message
// measured in the working relay capture.
func (s *Server) peerMailboxDocument(recipientName string, peer *session, source *syncpb.Document) *syncpb.Document {
	rank := participantRank(peer)
	str := func(v string) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: v}}
	}
	integer := func(v int64) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: v}}
	}
	return &syncpb.Document{
		Name: recipientName,
		Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"suid":   str(peer.userID),
			"susid":  str(sessionID(peer.userSession)),
			"sussid": integer(rank),
			"suscid": integer(10000 + rank),
			"pl":     proto.Clone(source.GetFields().GetFields()["pl"]).(*commonpb.Value),
		}},
		CreateTime: source.GetCreateTime(),
		UpdateTime: source.GetUpdateTime(),
	}
}

func (s *Server) syntheticSessionDocument(caller *session, name string) (*syncpb.Document, bool) {
	id := sessionID(caller.userSession)
	if id == "" {
		return nil, false
	}
	str := func(v string) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: v}}
	}
	integer := func(v int64) *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: v}}
	}
	emptyMap := func() *commonpb.Value {
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{}}}}
	}
	var fields map[string]*commonpb.Value
	switch name {
	case "docs/__us/" + id, "docs/__pgn/All/__pus/" + id:
		// The client's UserSession reader requires these typed fields in its
		// first watch snapshot. Keep attributes empty: a mismatched type is fatal.
		rank := participantRank(caller)
		fields = map[string]*commonpb.Value{
			"uid": str(caller.userID), "ussid": integer(rank), "ucsid": integer(rank),
			"upcsid": integer(rank), "pgn": str("All"), "st": integer(2),
			"tn": str(""), "att": emptyMap(), "ltc": emptyMap(),
		}
	case "docs/__pgn/All/__stu/" + id:
		// The working private-room capture starts this watch with empty pl/mp
		// maps and string session IDs. Once a console writes its own P2P pl
		// bytes, the stored document takes precedence over these defaults.
		fields = map[string]*commonpb.Value{
			"suid": str(caller.userID), "susid": str(id), "sussid": str(id),
			"suscid": str("1"), "pl": emptyMap(), "mp": emptyMap(),
		}
	default:
		return nil, false
	}
	return &syncpb.Document{
		Name: name, Fields: &commonpb.MapValue{Fields: fields},
		CreateTime: timestamppb.New(caller.createdAt),
		UpdateTime: timestamppb.New(caller.createdAt),
	}, true
}

func (s *Server) normalizeSessionChanges(caller *session, changes []*syncpb.DocumentChange) []*syncpb.DocumentChange {
	for _, change := range changes {
		if change.GetDocumentChangeType() == syncpb.DocumentChange_DELETED {
			continue
		}
		if doc, ok := s.watchDocument(caller, change.GetDocument().GetName()); ok {
			change.Document = doc
		}
	}
	return changes
}
