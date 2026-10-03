package gamesync

import (
	"context"
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
	w.publish("room", []*syncpb.DocumentChange{{
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

func TestAPiaPacketReachesOnlyTheRecipientsInboxAsWritten(t *testing.T) {
	h := newHarness(t)
	s := h.srv
	writer, err := s.newSession("alice", "userSessions/us-alice")
	if err != nil {
		t.Fatal(err)
	}
	writer.details = &SessionDetails{SessionID: "room", Players: []string{"alice", "bob"}}
	inbox := func(user, usid string) *subscriber {
		sub := s.watch.subscribe(user, "userSessions/"+usid, "room")
		t.Cleanup(func() { s.watch.unsubscribe(sub) })
		s.watch.addTarget(sub, &syncpb.Target{Name: "userSessions/current/targets/9", TargetType: &syncpb.Target_Documents{
			Documents: &syncpb.DocumentsTarget{Documents: []string{"docs/__pgn/All/__stu/" + usid}},
		}})
		return sub
	}
	bob, alice := inbox("bob", "us-bob"), inbox("alice", "us-alice")

	pl := []byte{1, 2, 3}
	ctx := context.WithValue(context.Background(), callerKey{}, writer)
	if _, err := s.WriteDocuments(ctx, &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{{
		OperationType: &syncpb.WriteOperation_UpdateDocument{UpdateDocument: &syncpb.UpdateDocumentRequest{
			Document: &syncpb.Document{Name: "docs/__pgn/All/__stu/us-bob", Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"pl":     {ValueType: &commonpb.Value_BytesValue{BytesValue: pl}},
				"suid":   {ValueType: &commonpb.Value_StringValue{StringValue: "alice"}},
				"susid":  {ValueType: &commonpb.Value_StringValue{StringValue: "us-alice"}},
				"suscid": {ValueType: &commonpb.Value_IntegerValue{IntegerValue: 1}},
				"sussid": {ValueType: &commonpb.Value_IntegerValue{IntegerValue: 1}},
			}}},
		}},
	}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-bob.changes:
		f := got.GetDocument().GetFields().GetFields()
		if got.GetTargetId() != "9" || got.GetDocument().GetName() != "docs/__pgn/All/__stu/us-bob" ||
			f["susid"].GetStringValue() != "us-alice" || f["suscid"].GetIntegerValue() != 1 ||
			string(f["pl"].GetBytesValue()) != string(pl) {
			t.Fatalf("inbox delivery = %v", got)
		}
	default:
		t.Fatal("recipient received no inbox update")
	}
	select {
	case got := <-alice.changes:
		t.Fatalf("writer received its own Pia packet back: %v", got)
	default:
	}
}
