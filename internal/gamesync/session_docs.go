package gamesync

import (
	"sort"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	syncpb "github.com/n-popescu/switchnet/proto/gamesync/v1"
)

func sessionID(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func wireTargetID(name string) string { return sessionID(name) }

func (s *Server) collectionSnapshot(caller *session, collection string) []*syncpb.Document {
	docs := s.docs.list(collection)
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
	if doc, ok := s.docs.get(name); ok {
		return doc, true
	}
	if doc, ok := s.roomDocument(caller, name); ok {
		return doc, true
	}
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
		fields = map[string]*commonpb.Value{
			"uid": str(caller.userID), "ussid": integer(1), "ucsid": integer(1),
			"upcsid": integer(1), "pgn": str("All"), "st": integer(2),
			"tn": str(""), "att": emptyMap(), "ltc": emptyMap(),
		}
	case "docs/__pgn/All/__stu/" + id:
		// The player-state reader expects a bytes-valued pl field. Once the
		// console writes its real P2P blob, the stored document takes precedence.
		fields = map[string]*commonpb.Value{
			"suid": str(caller.userID), "susid": str(id), "sussid": integer(1),
			"suscid": integer(10001),
			"pl":     &commonpb.Value{ValueType: &commonpb.Value_BytesValue{BytesValue: []byte{}}},
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
