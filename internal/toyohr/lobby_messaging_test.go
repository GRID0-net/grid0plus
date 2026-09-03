package toyohr

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// fakeRecvMessageStream is the minimal grpc.ServerStreamingServer fake
// RecvMessage needs: a cancellable context and a channel of what was sent,
// with none of the transport machinery a real *grpc.Server would provide.
type fakeRecvMessageStream struct {
	ctx  context.Context
	sent chan *toyohrpb.RecvMessageResponse
}

func newFakeRecvMessageStream(ctx context.Context) *fakeRecvMessageStream {
	return &fakeRecvMessageStream{ctx: ctx, sent: make(chan *toyohrpb.RecvMessageResponse, 16)}
}

func (f *fakeRecvMessageStream) Send(m *toyohrpb.RecvMessageResponse) error {
	f.sent <- m
	return nil
}
func (f *fakeRecvMessageStream) Context() context.Context     { return f.ctx }
func (f *fakeRecvMessageStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeRecvMessageStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeRecvMessageStream) SetTrailer(metadata.MD)       {}
func (f *fakeRecvMessageStream) SendMsg(m any) error          { return nil }
func (f *fakeRecvMessageStream) RecvMsg(m any) error          { return nil }

// TestSendLobbyMessageReachesAConcurrentRecvMessageStream is the end-to-end
// property internal/lobbymessaging's package comment describes: membership
// is "currently subscribed", so a console whose RecvMessage call is already
// running when SendLobbyMessage fires must see it live, not just on the
// next Drain.
func TestSendLobbyMessageReachesAConcurrentRecvMessageStream(t *testing.T) {
	svc, _ := newService(t, time.Now())

	ctx, cancel := context.WithCancel(callerCtx("alice000000000a"))
	defer cancel()
	stream := newFakeRecvMessageStream(ctx)

	done := make(chan error, 1)
	go func() {
		done <- svc.RecvMessage(&toyohrpb.RecvMessageRequest{Lobby: "lobby-1"}, stream)
	}()

	// Give RecvMessage a moment to Subscribe before sending -- otherwise the
	// send could race ahead of the subscription and only be visible on the
	// resume-token catch-up path, which is a different (also correct, but
	// not what this test checks) delivery route.
	time.Sleep(20 * time.Millisecond)

	if _, err := svc.SendLobbyMessage(callerCtx("bob0000000000b1"), &toyohrpb.SendLobbyMessageRequest{
		Lobby: "lobby-1",
		MessageBody: &toyohrpb.MessageBody{
			MessageRequestId: "req-1", MessageType: "chat",
		},
	}); err != nil {
		t.Fatalf("SendLobbyMessage: %v", err)
	}

	select {
	case got := <-stream.sent:
		msg := got.GetMessage()
		if msg == nil {
			t.Fatalf("first send was not a message: %+v", got)
		}
		if msg.GetMessageBody().GetMessageRequestId() != "req-1" {
			t.Errorf("message_request_id = %q, want req-1", msg.GetMessageBody().GetMessageRequestId())
		}
		if msg.GetSenderUser() != "bob0000000000b1" {
			t.Errorf("sender_user = %q, want bob0000000000b1", msg.GetSenderUser())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendLobbyMessage never reached the concurrent RecvMessage stream")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RecvMessage returned %v after its context was cancelled, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RecvMessage did not return after its context was cancelled")
	}
}

// TestSendLobbyMessageIsRetainedForARecvMessageThatConnectsLater covers the
// catch-up path: a lobby's topic retains what was sent before anyone was
// listening, the same "everything currently retained" rule
// internal/messaging's mailbox follows for an empty resume token.
func TestSendLobbyMessageIsRetainedForARecvMessageThatConnectsLater(t *testing.T) {
	svc, _ := newService(t, time.Now())

	if _, err := svc.SendLobbyMessage(callerCtx("alice000000000a"), &toyohrpb.SendLobbyMessageRequest{
		Lobby:       "lobby-2",
		MessageBody: &toyohrpb.MessageBody{MessageRequestId: "req-2"},
	}); err != nil {
		t.Fatalf("SendLobbyMessage: %v", err)
	}

	ctx, cancel := context.WithTimeout(callerCtx("bob0000000000b1"), 2*time.Second)
	defer cancel()
	stream := newFakeRecvMessageStream(ctx)

	go svc.RecvMessage(&toyohrpb.RecvMessageRequest{Lobby: "lobby-2"}, stream)

	select {
	case got := <-stream.sent:
		if got.GetMessage().GetMessageBody().GetMessageRequestId() != "req-2" {
			t.Errorf("got %+v, want req-2 replayed from before RecvMessage connected", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a message sent before RecvMessage connected was never replayed")
	}
}

func TestRecvMessageRequiresALobby(t *testing.T) {
	svc, _ := newService(t, time.Now())
	err := svc.RecvMessage(&toyohrpb.RecvMessageRequest{},
		newFakeRecvMessageStream(callerCtx("alice000000000a")))
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("RecvMessage with no lobby = %v, want InvalidArgument", err)
	}
}

func TestRecvMessageRequiresAuthentication(t *testing.T) {
	svc, _ := newService(t, time.Now())
	err := svc.RecvMessage(&toyohrpb.RecvMessageRequest{Lobby: "lobby-1"},
		newFakeRecvMessageStream(context.Background()))
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("RecvMessage with no caller in context = %v, want Unauthenticated", err)
	}
}

func TestSendLobbyMessageRequiresAMessageBody(t *testing.T) {
	svc, _ := newService(t, time.Now())
	_, err := svc.SendLobbyMessage(callerCtx("alice000000000a"),
		&toyohrpb.SendLobbyMessageRequest{Lobby: "lobby-1"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("SendLobbyMessage with no message_body = %v, want InvalidArgument", err)
	}
}

// TestLobbyMessagingSendMessageIsUnimplemented guards against silently
// "fixing" this later without reading the TODO(capture-needed) in
// lobby_messaging.go first -- see that note and internal/lobbymessaging's
// README for why it stays this way.
func TestLobbyMessagingSendMessageIsUnimplemented(t *testing.T) {
	svc, _ := newService(t, time.Now())
	_, err := svc.SendMessage(callerCtx("alice000000000a"), &toyohrpb.SendMessageRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("SendMessage = %v, want Unimplemented", err)
	}
}
