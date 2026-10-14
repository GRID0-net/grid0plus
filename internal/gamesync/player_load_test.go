package gamesync

import (
	"context"
	"fmt"
	"testing"
	"time"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"google.golang.org/grpc/metadata"
)

// Exercise real gRPC framing and flow control, with all players writing at
// once and one receiver deliberately draining slowly. Synthetic packet bytes
// test transport integrity; they do not simulate a successful Pia handshake.
func TestPeerDeliveryWithMorePlayersAndASlowReceiver(t *testing.T) {
	for _, count := range []int{2, 4, 8} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			h := newHarness(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			contexts := make([]context.Context, count)
			streams := make([]syncpb.Gamesync_KeepUserSessionClient, count)
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("player%d", i)
				caller, err := h.srv.newSession(id, "userSessions/"+id)
				if err != nil {
					t.Fatal(err)
				}
				caller.details = &SessionDetails{UserID: id, SessionID: "room"}
				contexts[i] = h.authed(caller.accessToken)
				stream, err := h.client.KeepUserSession(contextWithDeadline(contexts[i], ctx))
				if err != nil {
					t.Fatal(err)
				}
				streams[i] = stream
				if err := stream.Send(&syncpb.KeepUserSessionRequest{RequestType: &syncpb.KeepUserSessionRequest_UpdateTarget{UpdateTarget: &syncpb.UpdateTargetRequest{Target: &syncpb.Target{Name: "targets/9", TargetType: &syncpb.Target_Documents{Documents: &syncpb.DocumentsTarget{Documents: []string{peerInboxPrefix + id}}}}}}}); err != nil {
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
			}
			const rounds = 6
			errors := make(chan error, 2*count)
			for receiver, stream := range streams {
				go func(receiver int, stream syncpb.Gamesync_KeepUserSessionClient) {
					seen := map[string]bool{}
					for len(seen) < (count-1)*rounds {
						msg, err := stream.Recv()
						if err != nil {
							errors <- err
							return
						}
						d := msg.GetDocumentChange().GetDocument()
						if d == nil {
							continue
						}
						f := d.GetFields().GetFields()
						payload := f["pl"].GetBytesValue()
						if len(payload) != 32<<10 || int(payload[0]) == receiver || int(payload[1]) != receiver || f["suid"].GetStringValue() != fmt.Sprintf("player%d", payload[0]) {
							errors <- fmt.Errorf("misrouted or damaged packet for receiver %d", receiver)
							return
						}
						key := fmt.Sprintf("%d/%d", payload[0], payload[2])
						if seen[key] {
							errors <- fmt.Errorf("duplicate packet %s", key)
							return
						}
						seen[key] = true
						if receiver == count-1 {
							time.Sleep(10 * time.Millisecond)
						}
					}
					errors <- nil
				}(receiver, stream)
			}
			for sender := range contexts {
				go func(sender int) {
					for round := 0; round < rounds; round++ {
						var ops []*syncpb.WriteOperation
						for receiver := range contexts {
							if sender == receiver {
								continue
							}
							payload := make([]byte, 32<<10)
							payload[0], payload[1], payload[2] = byte(sender), byte(receiver), byte(round)
							id := fmt.Sprintf("player%d", sender)
							ops = append(ops, merge(doc(peerInboxPrefix+fmt.Sprintf("player%d", receiver), map[string]*commonpb.Value{"suid": stringValue(id), "susid": stringValue(id), "pl": {ValueType: &commonpb.Value_BytesValue{BytesValue: payload}}})))
						}
						if _, err := h.client.WriteDocuments(contextWithDeadline(contexts[sender], ctx), &syncpb.WriteDocumentsRequest{WriteOperations: ops}); err != nil {
							errors <- err
							return
						}
					}
					errors <- nil
				}(sender)
			}
			for i := 0; i < 2*count; i++ {
				select {
				case err := <-errors:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
		})
	}
}

func contextWithDeadline(auth, parent context.Context) context.Context {
	md, _ := metadata.FromOutgoingContext(auth)
	return metadata.NewOutgoingContext(parent, md)
}
