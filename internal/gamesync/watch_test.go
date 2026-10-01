package gamesync

import (
	"log/slog"
	"testing"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

func TestSessionInfoWriteReachesBothCollectionTargets(t *testing.T) {
	w := newWatchers()
	sub := w.subscribe("alice", "alice-us", "room")
	defer w.unsubscribe(sub)
	for _, id := range []string{"2", "4"} {
		w.addTarget(sub, &syncpb.Target{
			Name: "userSessions/current/targets/" + id,
			TargetType: &syncpb.Target_Collection{Collection: &syncpb.CollectionTarget{
				Collection: "docs/SessionInfo",
			}},
		})
	}
	w.publish([]*syncpb.DocumentChange{{
		DocumentChangeType: syncpb.DocumentChange_UPDATED,
		Document:           &syncpb.Document{Name: "docs/SessionInfo/bob"},
	}})
	for _, want := range []string{"2", "4"} {
		select {
		case got := <-sub.changes:
			if got.GetTargetId() != want || got.GetDocument().GetName() != "docs/SessionInfo/bob" {
				t.Fatalf("change = %v, want ready-state update on target %s", got, want)
			}
		default:
			t.Fatalf("no ready-state update on target %s", want)
		}
	}
}

func TestAConsoleWriteImmediatelyReachesThePeersPiaMailbox(t *testing.T) {
	s := &Server{watch: newWatchers(), log: slog.Default()}
	writer := &session{userID: "alice", userSession: "userSessions/us-alice",
		details: &SessionDetails{SessionID: "room", Players: []string{"alice", "bob"}}}
	peer := s.watch.subscribe("bob", "userSessions/us-bob", "room")
	defer s.watch.unsubscribe(peer)
	s.watch.addTarget(peer, &syncpb.Target{Name: "targets/9", TargetType: &syncpb.Target_Documents{
		Documents: &syncpb.DocumentsTarget{Documents: []string{"docs/__pgn/All/__stu/us-bob"}},
	}})
	pl := []byte{1, 2, 3}
	s.publishPeerMailbox(writer, []*syncpb.DocumentChange{{
		DocumentChangeType: syncpb.DocumentChange_UPDATED,
		Document: &syncpb.Document{Name: "docs/__pgn/All/__stu/us-alice",
			Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"pl": {ValueType: &commonpb.Value_BytesValue{BytesValue: pl}},
			}}},
	}})
	select {
	case got := <-peer.changes:
		if got.GetTargetId() != "9" || got.GetDocument().GetName() != "docs/__pgn/All/__stu/us-bob" ||
			got.GetDocument().GetFields().GetFields()["susid"].GetStringValue() != "us-alice" ||
			string(got.GetDocument().GetFields().GetFields()["pl"].GetBytesValue()) != string(pl) {
			t.Fatalf("mailbox = %v", got)
		}
	default:
		t.Fatal("peer received no mailbox update")
	}
}
