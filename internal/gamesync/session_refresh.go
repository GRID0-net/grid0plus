package gamesync

import (
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

type refreshTarget struct {
	name       string
	documents  []string
	collection string
}

// The working private-room watch publishes live session state after its
// initial LISTED snapshot. Splatoon's client is a passive watcher while it
// finishes NAT setup; if these synthetic documents are only sent once, the
// room operation can remain pending until its communication-error timeout.
func (s *Server) refreshSessionDocuments(caller *session, sub *subscriber, last map[string]*commonpb.MapValue) []*syncpb.KeepUserSessionResponse {
	sub.mu.Lock()
	var targets []refreshTarget
	for name, documents := range sub.documents {
		for _, doc := range documents {
			if doc == "docs/__stg/All" || strings.HasPrefix(doc, "docs/__pgn/All/__stu/") ||
				strings.HasPrefix(doc, "docs/__us/") {
				targets = append(targets, refreshTarget{name: name, documents: append([]string(nil), documents...)})
				break
			}
		}
	}
	for name, collection := range sub.collections {
		switch collection {
		case "docs/__gs", "docs/__us", "docs/__pgn/All/__pus":
			targets = append(targets, refreshTarget{name: name, collection: collection})
		}
	}
	sub.mu.Unlock()
	sort.Slice(targets, func(i, j int) bool { return targets[i].name < targets[j].name })

	var messages []*syncpb.KeepUserSessionResponse
	for _, target := range targets {
		var docs []*syncpb.Document
		if target.collection != "" {
			docs = s.collectionSnapshot(caller, target.collection)
		} else {
			docs = s.watchedDocuments(caller, target.documents)
		}
		id := wireTargetID(target.name)
		pushed := false
		for _, doc := range docs {
			name := doc.GetName()
			// The fixed session identity, expiry, and room key do not change
			// while the client waits. The working stream refreshes the mutable
			// settings, roster, and the caller's session instead.
			if target.collection == "docs/__gs" &&
				name != "docs/__gs/m" && name != "docs/__gs/r" && name != "docs/__gs/s" {
				continue
			}
			// Keep the periodic sweep for roster changes, but never send the
			// same Pia/session state every three seconds. The working server's
			// stream is mostly quiet between actual client writes; repeating
			// these documents can starve the peer connection on the same socket.
			key := id + "\x00" + name + "\x00" + doc.GetFields().GetFields()["susid"].GetStringValue()
			if proto.Equal(last[key], doc.GetFields()) {
				continue
			}
			last[key] = proto.Clone(doc.GetFields()).(*commonpb.MapValue)
			fresh := proto.Clone(doc).(*syncpb.Document)
			fresh.UpdateTime = timestamppb.New(s.now())
			kind := syncpb.DocumentChange_UPDATED
			if target.collection == "docs/__pgn/All/__pus" {
				kind = syncpb.DocumentChange_EXIST
			}
			messages = append(messages, &syncpb.KeepUserSessionResponse{
				ResponseType: &syncpb.KeepUserSessionResponse_DocumentChange{
					DocumentChange: &syncpb.DocumentChange{
						TargetId: id, DocumentChangeType: kind, Document: fresh,
					},
				},
			})
			pushed = true
		}
		if pushed {
			// The client buffers changes until it receives this boundary.
			messages = append(messages, &syncpb.KeepUserSessionResponse{
				ResponseType: &syncpb.KeepUserSessionResponse_TargetChange{
					TargetChange: &syncpb.TargetChange{
						TargetId: id, TargetChangeType: syncpb.TargetChange_UPDATED,
					},
				},
			})
		}
	}
	return messages
}
