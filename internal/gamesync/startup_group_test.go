package gamesync

import (
	"context"
	"fmt"
	"testing"
	"time"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Exercise the actual RPCs and per-recipient watch delivery. A host-only
// request must not override the authenticated match roster; a slow final peer
// blocks every earlier peer without requiring the nominal eight-player lobby.
func TestPublicKickoffWaitsForTheWholeMatchedGroup(t *testing.T) {
	for _, count := range []int{1, 2, 4, 8} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			h := newHarness(t)
			const battle = "20261014T190000_match"
			name := refereeResultPrefix + battle
			players := []string{}
			ids := map[string]string{}
			for i := 0; i < count; i++ {
				uid := fmt.Sprintf("player-%d", i)
				players = append(players, uid)
				ids[uid] = "us-" + uid
			}
			var peers []*session
			var streams []syncpb.Gamesync_KeepUserSessionClient
			for _, uid := range players {
				peer, err := h.srv.newSession(uid, "userSessions/"+ids[uid])
				if err != nil {
					t.Fatal(err)
				}
				peer.details = &SessionDetails{SessionID: "match", Mode: "coop_bigrun_config", Players: players, PlayerSessions: ids}
				peers = append(peers, peer)
				ctx, cancel := context.WithTimeout(h.authed(peer.accessToken), 5*time.Second)
				defer cancel()
				stream, err := h.client.KeepUserSession(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err := stream.Send(&syncpb.KeepUserSessionRequest{RequestType: &syncpb.KeepUserSessionRequest_UpdateTarget{UpdateTarget: &syncpb.UpdateTargetRequest{Target: &syncpb.Target{Name: "targets/9", TargetType: &syncpb.Target_Collection{Collection: &syncpb.CollectionTarget{Collection: "docs/@@RefereeResult"}}}}}}); err != nil {
					t.Fatal(err)
				}
				for {
					msg, err := stream.Recv()
					if err != nil {
						t.Fatal(err)
					}
					if msg.GetTargetChange().GetTargetChangeType() == syncpb.TargetChange_LISTED {
						break
					}
				}
				streams = append(streams, stream)
			}
			write := func(peer *session, d *syncpb.Document) {
				t.Helper()
				if _, err := h.client.WriteDocuments(h.authed(peer.accessToken), &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{merge(d)}}); err != nil {
					t.Fatal(err)
				}
			}
			// Deliberately request only the host: the server's match roster wins.
			write(peers[0], doc(refereeRequestPrefix+battle, map[string]*commonpb.Value{"users": {ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.Value{stringValue(players[0])}}}}}))
			watchStatus := func(want int64) {
				t.Helper()
				for i, stream := range streams {
					for {
						msg, err := stream.Recv()
						if err != nil {
							t.Fatal(err)
						}
						if change := msg.GetDocumentChange(); change != nil && change.GetDocument().GetName() == name {
							if got := change.GetDocument().GetFields().GetFields()["status"].GetIntegerValue(); got != want {
								t.Fatalf("peer %d watch status %d, want %d", i, got, want)
							}
							break
						}
					}
				}
			}
			watchStatus(-1)
			for i, peer := range peers {
				write(peer, doc(refereeProfilePrefix+battle+"/User/"+ids[peer.userID], map[string]*commonpb.Value{}))
				want := int64(-1)
				if i == count-1 {
					want = 0
				}
				watchStatus(want)
				for _, recipient := range peers {
					got, err := h.client.GetDocument(h.authed(recipient.accessToken), &syncpb.GetDocumentRequest{Name: name})
					if err != nil {
						t.Fatal(err)
					}
					if got.GetFields().GetFields()["status"].GetIntegerValue() != want {
						t.Fatal("read bypassed group readiness")
					}
					for _, snapshot := range h.srv.collectionSnapshot(recipient, "docs/@@RefereeResult") {
						if snapshot.GetFields().GetFields()["status"].GetIntegerValue() != want {
							t.Fatal("refresh bypassed group readiness")
						}
					}
				}
			}
			stored, _ := h.srv.docsFor(peers[0]).get(name)
			if stored.GetFields().GetFields()["status"].GetIntegerValue() != -1 {
				t.Fatal("projection mutated shared verdict")
			}
		})
	}
}

func TestMatchedPeerDisconnectCannotReleaseTheHost(t *testing.T) {
	h := newHarness(t)
	peer, _ := h.srv.newSession("alice", "userSessions/us-alice")
	peer.details = &SessionDetails{SessionID: "match", Mode: "coop_bigrun_config", Players: []string{"alice", "bob"}, PlayerSessions: map[string]string{"alice": "us-alice", "bob": "us-bob"}}
	const battle = "battle_match"
	h.srv.freezeKickoffPeers(peer, battle)
	h.srv.docsFor(peer).seed(doc(refereeProfilePrefix+battle+"/User/us-alice", map[string]*commonpb.Value{}))
	if h.srv.docsFor(peer).kickoffReady(battle, refereeProfilePrefix+battle+"/User/us-alice") {
		t.Fatal("missing peer released host")
	}
	// A disconnected peer stays in the initial group; a retry cannot shrink it.
	peer.details.Players = []string{"alice"}
	h.srv.freezeKickoffPeers(peer, battle)
	if h.srv.docsFor(peer).kickoffReady(battle, refereeProfilePrefix+battle+"/User/us-alice") {
		t.Fatal("retry shrank frozen group")
	}
}

func TestAgainKickoffDoesNotWaitForPlayersWhoAlreadyLeft(t *testing.T) {
	h := newHarness(t)
	peer, _ := h.srv.newSession("alice", "userSessions/us-alice")
	peer.details = &SessionDetails{SessionID: "match", Mode: "regular_match_config", Players: []string{"alice", "bob", "carol"}, PlayerSessions: map[string]string{"alice": "us-alice", "bob": "us-bob", "carol": "us-carol"}}
	sub := h.srv.watch.subscribe("bob", "userSessions/us-bob", "match")
	defer h.srv.watch.unsubscribe(sub)
	h.srv.docsFor(peer).seed(doc(refereeResultPrefix+"old_match", map[string]*commonpb.Value{"status": intValue(1)}))
	h.srv.freezeKickoffPeers(peer, "again_match")
	h.srv.watch.unsubscribe(sub)
	h.srv.docsFor(peer).seed(doc(refereeProfilePrefix+"again_match/User/us-alice", map[string]*commonpb.Value{}))
	if h.srv.docsFor(peer).kickoffReady("again_match", refereeProfilePrefix+"again_match/User/us-alice") {
		t.Fatal("bob's stream loss released the host")
	}
	for _, usid := range []string{"us-alice", "us-bob"} {
		h.srv.docsFor(peer).seed(doc(refereeProfilePrefix+"again_match/User/"+usid, map[string]*commonpb.Value{}))
	}
	if !h.srv.docsFor(peer).kickoffReady("again_match", refereeProfilePrefix+"again_match/User/us-alice") {
		t.Fatal("departed carol blocked rematch")
	}
}

func TestAClientCannotSubmitAnotherMatchedPlayersReadyProfile(t *testing.T) {
	h := newHarness(t)
	peer, _ := h.srv.newSession("alice", "userSessions/us-alice")
	peer.details = &SessionDetails{SessionID: "match"}
	for _, name := range []string{refereeProfilePrefix + "battle_match/User/us-bob", refereeProfilePrefix + "battle_other/User/us-alice"} {
		_, err := h.client.WriteDocuments(h.authed(peer.accessToken), &syncpb.WriteDocumentsRequest{WriteOperations: []*syncpb.WriteOperation{merge(doc("docs/marker", map[string]*commonpb.Value{})), merge(doc(name, map[string]*commonpb.Value{}))}})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("forged profile accepted: %v", err)
		}
		if h.srv.docsFor(peer).has("docs/marker") {
			t.Fatal("rejected batch partially committed")
		}
	}
}
