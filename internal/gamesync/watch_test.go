package gamesync

import (
	"testing"

	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

func TestSessionInfoWriteReachesBothCollectionTargets(t *testing.T) {
	w := newWatchers()
	sub := w.subscribe("alice", "room")
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
