package npln

import (
	"testing"

	messagingpb "github.com/n-popescu/switchnet/proto/messaging/v1"
)

// TestSendMessageIsDeliveredOnAFreshRecvMessageCall is the invite-mailbox
// property internal/npln/README.md describes: a message sent while the
// receiver was not connected is still there the moment it opens its mailbox.
func TestSendMessageIsDeliveredOnAFreshRecvMessageCall(t *testing.T) {
	h := newHarness(t)
	h.createUser("sender0000000001", "device-sender")
	h.createUser("receiver00000001", "device-receiver")

	senderCtx := h.ctx(h.sessionFor(t, "sender0000000001", "device-sender"), "")
	client := messagingpb.NewMessagingClient(h.conn)
	if _, err := client.SendMessage(senderCtx, &messagingpb.SendMessageRequest{
		ReceiverUsers: []string{"users/receiver00000001"},
		MessageBody: &messagingpb.MessageBody{
			MessageRequestId: "req-1",
			MessageType:      "invite",
			NeedAck:          true,
		},
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	receiverCtx := h.ctx(h.sessionFor(t, "receiver00000001", "device-receiver"), "")
	stream, err := client.RecvMessage(receiverCtx, &messagingpb.RecvMessageRequest{
		User: "users/receiver00000001",
	})
	if err != nil {
		t.Fatalf("RecvMessage: %v", err)
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	got := msg.GetMessage()
	if got == nil {
		t.Fatalf("first message is not a RecvMessage: %v", msg)
	}
	if got.GetMessageBody().GetMessageRequestId() != "req-1" {
		t.Errorf("message_request_id = %q, want req-1", got.GetMessageBody().GetMessageRequestId())
	}
	if got.GetSenderUser() != "users/sender0000000001" {
		t.Errorf("sender_user = %q, want users/sender0000000001", got.GetSenderUser())
	}
	if got.GetMessageResumeToken() == "" {
		t.Error("message_resume_token is empty")
	}
}

// TestSendAckReachesTheOriginalSendersOwnRecvMessageStream proves the
// end-to-end ack round trip through gRPC, not just internal/messaging's own
// routing rule: the sender's stream is what actually needs to see the ack.
func TestSendAckReachesTheOriginalSendersOwnRecvMessageStream(t *testing.T) {
	h := newHarness(t)
	h.createUser("sender0000000002", "device-sender-2")
	h.createUser("receiver00000002", "device-receiver-2")
	client := messagingpb.NewMessagingClient(h.conn)

	senderCtx := h.ctx(h.sessionFor(t, "sender0000000002", "device-sender-2"), "")
	senderStream, err := client.RecvMessage(senderCtx, &messagingpb.RecvMessageRequest{})
	if err != nil {
		t.Fatalf("RecvMessage (sender): %v", err)
	}

	if _, err := client.SendMessage(senderCtx, &messagingpb.SendMessageRequest{
		ReceiverUsers: []string{"users/receiver00000002"},
		MessageBody:   &messagingpb.MessageBody{MessageRequestId: "req-2", NeedAck: true},
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	receiverCtx := h.ctx(h.sessionFor(t, "receiver00000002", "device-receiver-2"), "")
	if _, err := client.SendAck(receiverCtx, &messagingpb.SendAckRequest{
		Acks: []*messagingpb.AckBody{
			{MessageRequestId: "req-2", SenderUser: "users/sender0000000002"},
		},
	}); err != nil {
		t.Fatalf("SendAck: %v", err)
	}

	ack, err := recvWithTimeout(t, senderStream)
	if err != nil {
		t.Fatalf("recv ack on the sender's own stream: %v", err)
	}
	got := ack.GetAck()
	if got == nil {
		t.Fatalf("pushed message is not an ack: %v", ack)
	}
	if got.GetAck().GetMessageRequestId() != "req-2" {
		t.Errorf("message_request_id = %q, want req-2", got.GetAck().GetMessageRequestId())
	}
	if got.GetReceiverUser() != "users/receiver00000002" {
		t.Errorf("receiver_user (who acked) = %q, want users/receiver00000002", got.GetReceiverUser())
	}
}

// TestRecvMessageResumesFromTheGivenTokenRatherThanRedelivering proves the
// resume token actually resumes rather than redelivering the whole mailbox
// on every reconnect.
func TestRecvMessageResumesFromTheGivenTokenRatherThanRedelivering(t *testing.T) {
	h := newHarness(t)
	h.createUser("sender0000000003", "device-sender-3")
	h.createUser("receiver00000003", "device-receiver-3")
	client := messagingpb.NewMessagingClient(h.conn)

	senderCtx := h.ctx(h.sessionFor(t, "sender0000000003", "device-sender-3"), "")
	if _, err := client.SendMessage(senderCtx, &messagingpb.SendMessageRequest{
		ReceiverUsers: []string{"users/receiver00000003"},
		MessageBody:   &messagingpb.MessageBody{MessageRequestId: "first"},
	}); err != nil {
		t.Fatalf("SendMessage (first): %v", err)
	}

	receiverCtx := h.ctx(h.sessionFor(t, "receiver00000003", "device-receiver-3"), "")
	first, err := client.RecvMessage(receiverCtx, &messagingpb.RecvMessageRequest{})
	if err != nil {
		t.Fatalf("RecvMessage: %v", err)
	}
	firstMsg, err := first.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	token := firstMsg.GetMessage().GetMessageResumeToken()

	if _, err := client.SendMessage(senderCtx, &messagingpb.SendMessageRequest{
		ReceiverUsers: []string{"users/receiver00000003"},
		MessageBody:   &messagingpb.MessageBody{MessageRequestId: "second"},
	}); err != nil {
		t.Fatalf("SendMessage (second): %v", err)
	}

	// Reconnect with the resume token from the first message: only the
	// second should be redelivered.
	second, err := client.RecvMessage(receiverCtx, &messagingpb.RecvMessageRequest{
		MessageResumeToken: token,
	})
	if err != nil {
		t.Fatalf("RecvMessage (resumed): %v", err)
	}
	secondMsg, err := second.Recv()
	if err != nil {
		t.Fatalf("recv (resumed): %v", err)
	}
	if got := secondMsg.GetMessage().GetMessageBody().GetMessageRequestId(); got != "second" {
		t.Errorf("first message after resuming = %q, want \"second\" (not a redelivery of \"first\")", got)
	}
}
