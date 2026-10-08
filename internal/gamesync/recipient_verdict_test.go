package gamesync

import (
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"testing"
)

func TestAVersusVerdictWaitsForEachRecipientsOwnResult(t *testing.T) {
	h := newHarness(t)
	alice, err := h.srv.newSession("alice", "userSessions/us-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := h.srv.newSession("bob", "userSessions/us-bob")
	if err != nil {
		t.Fatal(err)
	}
	alice.details = &SessionDetails{SessionID: "room"}
	bob.details = &SessionDetails{SessionID: "room"}
	name := refereeResultPrefix + "battle_room"
	fields := map[string]*commonpb.Value{"status": intValue(1), "personal_result": {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"a": {ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"npln_user_id": stringValue("alice")}}}}}}}}}
	_, commit, err := h.srv.docsFor(alice).apply([]*syncpb.WriteOperation{merge(doc(name, fields))})
	if err != nil {
		t.Fatal(err)
	}
	for _, caller := range []*session{alice, bob} {
		want := int64(0)
		if caller == alice {
			want = 1
		}
		read, err := h.client.GetDocument(h.authed(caller.accessToken), &syncpb.GetDocumentRequest{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		if got := read.GetFields().GetFields()["status"].GetIntegerValue(); got != want {
			t.Fatalf("%s read status %d, want %d", caller.userID, got, want)
		}
		sub := h.srv.watch.subscribe(caller.userID, caller.userSession, "room")
		h.srv.watch.addTarget(sub, &syncpb.Target{Name: "targets/9", TargetType: &syncpb.Target_Documents{Documents: &syncpb.DocumentsTarget{Documents: []string{name}}}})
		h.srv.watch.publish("room", commit.changes)
		select {
		case change := <-sub.changes:
			if got := change.GetDocument().GetFields().GetFields()["status"].GetIntegerValue(); got != want {
				t.Fatalf("%s live status %d, want %d", caller.userID, got, want)
			}
		default:
			t.Fatal("missing verdict")
		}
		h.srv.watch.unsubscribe(sub)
	}
	stored, _ := h.srv.docsFor(alice).snapshot(name)
	if stored.GetFields().GetFields()["status"].GetIntegerValue() != 1 {
		t.Fatal("recipient projection modified shared verdict")
	}
}
