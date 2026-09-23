package toyohr

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// recvStream is a minimal ServerStreamingServer that collects what is sent.
type recvStream struct {
	ctx  context.Context
	sent chan *toyohrpb.RecvMessageResponse
}

func newRecvStream(ctx context.Context) *recvStream {
	return &recvStream{ctx: ctx, sent: make(chan *toyohrpb.RecvMessageResponse, 16)}
}

func (r *recvStream) Send(m *toyohrpb.RecvMessageResponse) error {
	r.sent <- m
	return nil
}
func (r *recvStream) Context() context.Context     { return r.ctx }
func (r *recvStream) SetHeader(metadata.MD) error  { return nil }
func (r *recvStream) SendHeader(metadata.MD) error { return nil }
func (r *recvStream) SetTrailer(metadata.MD)       {}
func (r *recvStream) SendMsg(any) error            { return nil }
func (r *recvStream) RecvMsg(any) error            { return nil }

func messagingFor(t *testing.T) *lobbyMessagingService {
	t.Helper()
	svc, _ := newService(t, time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	return &lobbyMessagingService{s: svc}
}

// receive attaches a stream for one player and returns it, plus a cancel.
//
// The service reads the caller from testCaller, so the player is set for the
// duration of the call that attaches the stream -- which is when subscribe
// happens.
func receive(t *testing.T, l *lobbyMessagingService, userID, lobby string) (*recvStream, func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	stream := newRecvStream(ctx)

	previous := testCaller
	testCaller = userID
	started := make(chan struct{})
	go func() {
		close(started)
		_ = l.RecvMessage(&toyohrpb.RecvMessageRequest{Lobby: lobby}, stream)
	}()
	<-started

	// The handler sends a keep-alive as its first message, so waiting for that
	// is how a test knows the subscription is in place rather than sleeping.
	select {
	case first := <-stream.sent:
		if first.GetKeepAlive() == nil {
			t.Fatalf("the first message on a new stream is %T, want a keep-alive", first.GetPayload())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stream never sent its opening keep-alive")
	}
	testCaller = previous

	return stream, cancel
}

func TestAStreamOpensWithAKeepAliveRatherThanSilence(t *testing.T) {
	l := messagingFor(t)
	// receive asserts it: the response carries a KeepAlive in its own oneof,
	// which is the protocol saying silence is not a valid state. A client
	// holding a stream that has never spoken treats it as dead.
	_, cancel := receive(t, l, "alice", "lobby-1")
	cancel()
}

func TestTheSenderIsTakenFromTheTokenNotFromTheRequest(t *testing.T) {
	l := messagingFor(t)
	bob, cancel := receive(t, l, "bob", "lobby-1")
	defer cancel()

	as(t, "alice")
	if _, err := l.SendMessage(context.Background(), &toyohrpb.SendMessageRequest{
		User:          "carol",
		ReceiverUsers: []string{"bob"},
		MessageBody:   &toyohrpb.MessageBody{MessageType: "chat"},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	msg := waitForMessage(t, bob)
	if msg.GetSenderUser() != "alice" {
		t.Errorf("sender = %q, want alice — the authenticated caller, not the named one",
			msg.GetSenderUser())
	}
	if msg.GetMessageBody().GetMessageType() != "chat" {
		t.Errorf("the body did not survive delivery: %v", msg.GetMessageBody())
	}
	if msg.GetSendTime() == nil {
		t.Error("no send time")
	}
}

func TestALobbyMessageReachesTheLobbyButNotTheSender(t *testing.T) {
	l := messagingFor(t)
	alice, cancelAlice := receive(t, l, "alice", "lobby-1")
	defer cancelAlice()
	bob, cancelBob := receive(t, l, "bob", "lobby-1")
	defer cancelBob()
	carol, cancelCarol := receive(t, l, "carol", "lobby-2")
	defer cancelCarol()

	as(t, "alice")
	if _, err := l.SendLobbyMessage(context.Background(), &toyohrpb.SendLobbyMessageRequest{
		Lobby:       "lobby-1",
		MessageBody: &toyohrpb.MessageBody{MessageType: "signal"},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if got := waitForMessage(t, bob).GetSenderUser(); got != "alice" {
		t.Errorf("bob got a message from %q, want alice", got)
	}
	// The sender's own console already knows what it sent; echoing shows the
	// player their own message twice.
	assertNoMessage(t, alice, "the sender was echoed their own lobby message")
	// And another lobby is a different topic.
	assertNoMessage(t, carol, "a message crossed into another lobby")
}

func TestAMessageToNobodyListeningIsNotAnError(t *testing.T) {
	l := messagingFor(t)
	as(t, "alice")

	// A message to a player who just closed the game is a message nobody
	// receives, and reporting that as a failure has the sender's console retry
	// something that cannot succeed.
	if _, err := l.SendMessage(context.Background(), &toyohrpb.SendMessageRequest{
		ReceiverUsers: []string{"nobody"},
		MessageBody:   &toyohrpb.MessageBody{MessageType: "chat"},
	}); err != nil {
		t.Errorf("sending to an absent player errored: %v", err)
	}
}

func TestSendingWithNoReceiverOrNoLobbyIsRefused(t *testing.T) {
	l := messagingFor(t)
	as(t, "alice")

	if _, err := l.SendMessage(context.Background(), &toyohrpb.SendMessageRequest{
		MessageBody: &toyohrpb.MessageBody{},
	}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("no receivers gave %v, want InvalidArgument", err)
	}
	if _, err := l.SendLobbyMessage(context.Background(), &toyohrpb.SendLobbyMessageRequest{
		MessageBody: &toyohrpb.MessageBody{},
	}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("no lobby gave %v, want InvalidArgument", err)
	}
}

func TestAnUnauthenticatedCallCannotSendOrReceive(t *testing.T) {
	l := messagingFor(t)
	as(t, "")

	if err := l.RecvMessage(&toyohrpb.RecvMessageRequest{Lobby: "lobby-1"},
		newRecvStream(context.Background())); status.Code(err) != codes.Unauthenticated {
		t.Errorf("receiving with no caller gave %v, want Unauthenticated", err)
	}
	if _, err := l.SendMessage(context.Background(), &toyohrpb.SendMessageRequest{
		ReceiverUsers: []string{"bob"}, MessageBody: &toyohrpb.MessageBody{},
	}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("sending with no caller gave %v, want Unauthenticated", err)
	}
}

func TestAClosedStreamStopsReceiving(t *testing.T) {
	l := messagingFor(t)
	bob, cancel := receive(t, l, "bob", "lobby-1")
	cancel()

	// Give the handler a moment to unsubscribe, then confirm nothing is
	// delivered to a stream nobody is on -- a subscription that outlives its
	// stream is a slow leak of channels nothing drains.
	deadline := time.Now().Add(2 * time.Second)
	for l.s.messages.subscriberCount("bob") != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := l.s.messages.subscriberCount("bob"); n != 0 {
		t.Fatalf("bob still has %d subscriptions after his stream closed", n)
	}

	as(t, "alice")
	if _, err := l.SendMessage(context.Background(), &toyohrpb.SendMessageRequest{
		ReceiverUsers: []string{"bob"}, MessageBody: &toyohrpb.MessageBody{},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	assertNoMessage(t, bob, "a closed stream still received a message")
}

func TestAReportIsAcceptedAndAViolationIsNever(t *testing.T) {
	svc, _ := newService(t, time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	u := &userScreeningService{s: svc}
	as(t, "alice")

	// Accepted rather than refused: the console shows a failure here to the
	// player, and somebody reporting another player should not see a
	// communication error.
	report, err := u.CreateReport(context.Background(), &toyohrpb.CreateReportRequest{
		Report: &toyohrpb.Report{
			Category: "chat", Reason: "abuse", ScreeningTarget: "bob",
		},
	})
	if err != nil {
		t.Fatalf("create report: %v", err)
	}
	if report.GetCategory() != "chat" || report.GetCreateTime() == nil {
		t.Errorf("report = %+v, want the category echoed and a timestamp", report)
	}

	// SwitchNet issues no penalties: there is nobody to issue them, and a
	// private server that silently banned one of the operator's own consoles
	// would be a bug with no way to see it.
	violation, err := u.GetViolation(context.Background(), &toyohrpb.GetViolationRequest{})
	if err != nil {
		t.Errorf("get violation: %v", err)
	}
	// Regression test for a real crash: an empty name here makes Splatoon 3's
	// own resource-path parser hard-abort. See GetViolation's own doc comment.
	if !strings.HasSuffix(violation.GetName(), "/violations/current") {
		t.Errorf("name = %q, want it to end in a violations resource segment -- this is what crashes a real console", violation.GetName())
	}
}

// ---- helpers -------------------------------------------------------------

func waitForMessage(t *testing.T, stream *recvStream) *toyohrpb.ReceivedMessage {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-stream.sent:
			if msg := got.GetMessage(); msg != nil {
				return msg
			}
			// A keep-alive on the way past. Keep waiting for the message.
		case <-deadline:
			t.Fatal("no message arrived")
			return nil
		}
	}
}

func assertNoMessage(t *testing.T, stream *recvStream, what string) {
	t.Helper()
	deadline := time.After(250 * time.Millisecond)
	for {
		select {
		case got := <-stream.sent:
			if got.GetMessage() != nil {
				t.Error(what)
				return
			}
		case <-deadline:
			return
		}
	}
}
