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
	docs := s.docsFor(caller).list(collection)
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
	if doc, ok := s.docsFor(caller).get(name); ok {
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
				// The initial __stu snapshot has empty pl/mp maps. Once a
				// console writes a byte payload, the working stream omits mp
				// rather than carrying that initial default beside the bytes.
				if key == "mp" && strings.HasPrefix(name, "docs/__pgn/All/__stu/") &&
					len(merged.Fields.Fields["pl"].GetBytesValue()) > 0 {
					continue
				}
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

// watchedDocuments answers a documents target with exactly the documents it
// names.
//
// docs/__pgn/All/__stu/<usid> is that user session's Pia *inbox*: a peer writes
// its packet into the recipient's document and stamps suid/susid/suscid/sussid
// with its own identity (Nextendo capture: session 63790481 writes into
// __stu/b55e6b1f with its own susid, and the server delivers it as written). An
// earlier reading treated it as the writer's outbox and renamed every peer's
// document onto the caller's inbox. A peer's inbox holds the caller's own
// outgoing packets, so the caller got its own Pia packets back stamped "from
// the peer", and from then on addressed its packets to itself: the private
// room's ready step timed out with a communication error
// (docs/CAPTURES/raw/grid0-private-ready-analysis-20261002.md).
func (s *Server) watchedDocuments(caller *session, names []string) []*syncpb.Document {
	var docs []*syncpb.Document
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		if doc, ok := s.watchDocument(caller, name); ok {
			docs = append(docs, doc)
			seen[name] = true
		}
	}
	return docs
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
		if isPrivateBattle(caller.details) {
			// A Ryujinx private room on Nextendo, 2026-10-02: empty pl bytes,
			// integer ids, the private connection-id namespace (10000 + rank),
			// and no mp. Splatoon's Pia closed its session 0.3 s after reading
			// the older map-typed shape below, before the room was created.
			rank := participantRank(caller)
			fields = map[string]*commonpb.Value{
				"pl":     {ValueType: &commonpb.Value_BytesValue{BytesValue: []byte{}}},
				"suid":   str(caller.userID),
				"suscid": integer(10000 + rank),
				"susid":  str(id),
				"sussid": integer(rank),
			}
			break
		}
		// The working public-match capture starts this watch with empty pl/mp
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

// scopeOf is the game session a caller's documents belong to.
func scopeOf(caller *session) string {
	if caller == nil || caller.details == nil {
		return ""
	}
	return caller.details.SessionID
}

func (s *Server) docsFor(caller *session) *store { return s.docs.in(scopeOf(caller)) }

// rankSessionCounter answers the console's increment of its own upcsid with
// its rank in the game session.
//
// Each console joins the session by writing its __pus document and then
// transforming upcsid: load the global __upcsidn, maximum 20000, increment
// 1, store the global back. It keeps the increment's result as its own
// upcsid, and Pia then finds itself, and the host, among the __pus documents
// by upcsid (the loop at 0x7100a33040 compares each player's upcsid with its
// own and with the host's). grid0 delivers those documents with upcsid set
// to the rank, as Nextendo's server does, but answered the increment with
// the raw counter, 20001 and up. No console found itself, and each one
// hosted a mesh of its own: across networks that is SessionAlone.
// Nextendo's server answers the same increment with 1 (capture of
// 2026-09-30), so the result is the rank, the same number the documents
// carry.
func rankSessionCounter(caller *session, ops []*syncpb.WriteOperation, results []*syncpb.WriteResult) {
	if caller == nil || caller.details == nil || caller.details.SessionID == "" {
		return
	}
	own := "docs/__pgn/All/__pus/" + sessionID(caller.userSession)
	rank := participantRank(caller)
	for i, op := range ops {
		t := op.GetTransformDocument()
		if t == nil || t.GetName() != own || i >= len(results) {
			continue
		}
		values := results[i].GetTransformResult().GetResults()
		for j, ft := range t.GetFieldTransforms() {
			if j < len(values) && maskTopLevel(ft.GetFieldPath()) == "upcsid" && ft.GetIncrement() != nil {
				values[j] = &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: rank}}
			}
		}
	}
}
