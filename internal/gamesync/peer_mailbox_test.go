package gamesync

import (
	"context"
	"fmt"
	"testing"
	"time"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"google.golang.org/protobuf/proto"
)

func TestALateWatchReplaysEveryAuthenticatedPiaSender(t *testing.T) {
	h := newHarness(t)
	recipient, err := h.srv.newSession("bob", "userSessions/us-bob")
	if err != nil {
		t.Fatal(err)
	}
	recipient.details = &SessionDetails{SessionID: "room"}
	name := peerInboxPrefix + "us-bob"
	for _, user := range []string{"alice", "carol"} {
		sender, err := h.srv.newSession(user, "userSessions/us-"+user)
		if err != nil {
			t.Fatal(err)
		}
		sender.details = &SessionDetails{SessionID: "room"}
		ctx := context.WithValue(context.Background(), callerKey{}, sender)
		fields := map[string]*commonpb.Value{"suid": stringValue(user), "susid": stringValue("us-" + user), "pl": {ValueType: &commonpb.Value_BytesValue{BytesValue: []byte(user)}}}
		if _, err := h.srv.WriteDocuments(ctx, &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{merge(doc(name, fields))}}); err != nil {
			t.Fatal(err)
		}
	}
	// A watch reconnect must not collapse the recipient's multiple senders.
	h.srv.beginSessionStream(recipient)
	h.srv.endSessionStream(recipient)
	ctx, cancel := context.WithTimeout(h.authed(recipient.accessToken), time.Second)
	defer cancel()
	stream, err := h.client.KeepUserSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&syncpb.KeepUserSessionRequest{RequestType: &syncpb.KeepUserSessionRequest_UpdateTarget{UpdateTarget: &syncpb.UpdateTargetRequest{Target: &syncpb.Target{Name: "targets/9", TargetType: &syncpb.Target_Documents{Documents: &syncpb.DocumentsTarget{Documents: []string{name}}}}}}}); err != nil {
		t.Fatal(err)
	}
	senders := map[string]string{}
	for {
		response, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if d := response.GetDocumentChange().GetDocument(); d != nil {
			// Serialize the response as the transport does before inspecting fields.
			wire, err := proto.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			decoded := new(syncpb.Document)
			if err := proto.Unmarshal(wire, decoded); err != nil {
				t.Fatal(err)
			}
			f := decoded.GetFields().GetFields()
			senders[f["susid"].GetStringValue()] = string(f["pl"].GetBytesValue())
		}
		if response.GetTargetChange().GetTargetChangeType() == syncpb.TargetChange_LISTED {
			break
		}
	}
	if senders["us-alice"] != "alice" || senders["us-carol"] != "carol" {
		t.Fatalf("snapshot senders = %v", senders)
	}
}

func TestWatchOverflowCannotLookLikeSuccessfulDelivery(t *testing.T) {
	w := newWatchers()
	sub := w.subscribe("bob", "us-bob", "room")
	defer w.unsubscribe(sub)
	name := peerInboxPrefix + "us-bob"
	w.addTarget(sub, &syncpb.Target{Name: "targets/9", TargetType: &syncpb.Target_Documents{Documents: &syncpb.DocumentsTarget{Documents: []string{name}}}})
	for i := 0; i < 70; i++ {
		w.publish("room", []*syncpb.DocumentChange{{Document: doc(name, map[string]*commonpb.Value{"sequence": intValue(int64(i))})}})
	}
	select {
	case <-sub.overflow:
	default:
		t.Fatal("overflow silently discarded peer state")
	}
}

func TestALargeInitialWatchSnapshotEndsAfterEveryDocument(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(h.session("token-alice"), time.Second)
	defer cancel()
	var names []string
	var ops []*syncpb.WriteOperation
	for i := 0; i < 80; i++ {
		name := fmt.Sprintf("docs/test/%d", i)
		names = append(names, name)
		ops = append(ops, merge(doc(name, map[string]*commonpb.Value{"number": intValue(int64(i))})))
	}
	if _, err := h.client.WriteDocuments(ctx, &syncpb.WriteDocumentsRequest{WriteOperations: ops}); err != nil {
		t.Fatal(err)
	}
	stream, err := h.client.KeepUserSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&syncpb.KeepUserSessionRequest{RequestType: &syncpb.KeepUserSessionRequest_UpdateTarget{UpdateTarget: &syncpb.UpdateTargetRequest{Target: &syncpb.Target{Name: "targets/9", TargetType: &syncpb.Target_Documents{Documents: &syncpb.DocumentsTarget{Documents: names}}}}}}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for {
		response, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if d := response.GetDocumentChange().GetDocument(); d != nil {
			seen[d.GetName()] = true
		}
		if response.GetTargetChange().GetTargetChangeType() == syncpb.TargetChange_LISTED {
			break
		}
	}
	if len(seen) != len(names) {
		t.Fatalf("snapshot ended after %d of %d documents", len(seen), len(names))
	}
}

func TestAPiaNotificationKeepsItsCommittedSenderWhenAnotherWriteOvertakesIt(t *testing.T) {
	h := newHarness(t)
	caller, err := h.srv.newSession("bob", "userSessions/us-bob")
	if err != nil {
		t.Fatal(err)
	}
	caller.details = &SessionDetails{SessionID: "room"}
	name := peerInboxPrefix + "us-bob"
	_, first, err := h.srv.docsFor(caller).apply([]*syncpb.WriteOperation{merge(doc(name, map[string]*commonpb.Value{"susid": stringValue("us-alice"), "pl": {ValueType: &commonpb.Value_BytesValue{BytesValue: []byte("alice")}}}))})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.srv.docsFor(caller).apply([]*syncpb.WriteOperation{merge(doc(name, map[string]*commonpb.Value{"susid": stringValue("us-carol"), "pl": {ValueType: &commonpb.Value_BytesValue{BytesValue: []byte("carol")}}}))}); err != nil {
		t.Fatal(err)
	}
	changes := h.srv.normalizeSessionChanges(caller, first.changes)
	fields := changes[0].GetDocument().GetFields().GetFields()
	if fields["susid"].GetStringValue() != "us-alice" || string(fields["pl"].GetBytesValue()) != "alice" {
		t.Fatal("a committed notification was replaced by another sender's later packet")
	}
}
