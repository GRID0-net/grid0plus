package gamesync

import (
	"context"
	"testing"
	"time"

	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

func TestTheRoomWatchRepublishesMutableSessionDocumentsAfterListing(t *testing.T) {
	h := newHarness(t)
	alice := h.session("token-alice")
	h.srv.refreshEvery = 20 * time.Millisecond
	h.srv.mu.Lock()
	for _, sess := range h.srv.sessions {
		sess.details = &SessionDetails{
			UserID: "alice", SessionID: "room", UserSessionID: "alice-us",
			Tenant: "t-dce9377b-lp1", Host: "example.invalid", Port: 7575,
			Capacity: 10, ParticipantCount: 1, CreatedAt: h.now,
		}
	}
	h.srv.mu.Unlock()

	ctx, cancel := context.WithTimeout(alice, 2*time.Second)
	defer cancel()
	stream, err := h.client.KeepUserSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&syncpb.KeepUserSessionRequest{
		RequestType: &syncpb.KeepUserSessionRequest_UpdateTarget{
			UpdateTarget: &syncpb.UpdateTargetRequest{Target: &syncpb.Target{
				Name: "userSessions/current/targets/6",
				TargetType: &syncpb.Target_Collection{Collection: &syncpb.CollectionTarget{
					Collection: "docs/__gs",
				}},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	listed, liveStatus, boundary := false, false, false
	for !boundary {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("watch ended before a live session refresh: listed=%v status=%v: %v", listed, liveStatus, err)
		}
		if change := msg.GetTargetChange(); change != nil && change.GetTargetId() == "6" {
			if change.GetTargetChangeType() == syncpb.TargetChange_LISTED {
				listed = true
			} else if listed && liveStatus && change.GetTargetChangeType() == syncpb.TargetChange_UPDATED {
				boundary = true
			}
		}
		if change := msg.GetDocumentChange(); change != nil && listed &&
			change.GetDocument().GetName() == "docs/__gs/s" &&
			change.GetDocumentChangeType() == syncpb.DocumentChange_UPDATED {
			fields := change.GetDocument().GetFields().GetFields()
			if fields["uid"].GetStringValue() != "alice" || len(fields) != 9 || fields["bfmax"] != nil {
				t.Fatalf("live __gs/s is not the player's session state: fields=%v", fields)
			}
			liveStatus = true
		}
	}
}
