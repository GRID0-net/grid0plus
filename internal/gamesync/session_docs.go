package gamesync

import (
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

// watchDocument only synthesizes the caller's own UserSession. A missing
// document for another session is not evidence that the caller owns it.
func (s *Server) watchDocument(caller *session, name string) (*syncpb.Document, bool) {
	if doc, ok := s.docs.get(name); ok {
		return doc, true
	}
	id := sessionID(caller.userSession)
	if id == "" || name != "docs/__us/"+id {
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
	// The client's UserSession reader requires these typed fields in its first
	// watch snapshot. Keep the attribute maps empty: the game treats mismatched
	// attribute types as a fatal error.
	return &syncpb.Document{
		Name: name,
		Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"uid": str(caller.userID), "ussid": integer(1), "ucsid": integer(1),
			"upcsid": integer(1), "pgn": str("All"), "st": integer(2),
			"tn": str(""), "att": emptyMap(), "ltc": emptyMap(),
		}},
		CreateTime: timestamppb.New(caller.createdAt),
		UpdateTime: timestamppb.New(caller.createdAt),
	}, true
}
