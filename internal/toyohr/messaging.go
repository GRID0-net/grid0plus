package toyohr

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// lobbyMessagingService is nn.npln.toyohr.v1.LobbyMessaging: the messages
// players exchange in a lobby.
type lobbyMessagingService struct {
	toyohrpb.UnimplementedLobbyMessagingServer
	s *Service
}

// messageHub delivers lobby messages between connected players.
//
// In memory, and deliberately: a lobby message is worth nothing to a player who
// was not connected when it was sent — that is what makes it a message rather
// than a mailbox. Persisting them would mean deciding when they expire, and the
// answer is "immediately".
type messageHub struct {
	mu sync.Mutex
	// byUser are the streams each player is receiving on, keyed by user id. A
	// slice per player because one console can hold more than one stream open
	// across a reconnect, and delivering to only the newest loses the message
	// that arrives during the overlap.
	byUser map[string][]*subscriber
}

// subscriber is one open RecvMessage stream.
type subscriber struct {
	userID string
	// lobby is the lobby the stream is attached to, exactly as the client named
	// it. An opaque topic: which lobbies exist is the game's business, and
	// treating it as a string rather than modelling it means there is nothing
	// here to get out of step with the game.
	lobby string
	// ch is buffered, and a send that would block is dropped. A console that
	// has stopped reading must not be able to stall delivery for everybody
	// else -- the same trade internal/matchmaking's watchers make.
	ch chan *toyohrpb.ReceivedMessage
}

func newMessageHub() *messageHub {
	return &messageHub{byUser: map[string][]*subscriber{}}
}

func (h *messageHub) subscribe(userID, lobby string) *subscriber {
	sub := &subscriber{
		userID: userID,
		lobby:  lobby,
		ch:     make(chan *toyohrpb.ReceivedMessage, 16),
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.byUser[userID] = append(h.byUser[userID], sub)
	return sub
}

func (h *messageHub) unsubscribe(sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()

	list := h.byUser[sub.userID]
	for i, s := range list {
		if s == sub {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(h.byUser, sub.userID)
	} else {
		h.byUser[sub.userID] = list
	}
}

// sendTo delivers a message to named players, and reports how many streams took
// it.
func (h *messageHub) sendTo(users []string, msg *toyohrpb.ReceivedMessage) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	delivered := 0
	for _, user := range users {
		for _, sub := range h.byUser[user] {
			select {
			case sub.ch <- msg:
				delivered++
			default:
			}
		}
	}
	return delivered
}

// sendToLobby delivers a message to every stream attached to a lobby, except
// the sender's own.
//
// Excluding the sender because the game already knows what it sent; echoing it
// back shows the player their own message twice.
func (h *messageHub) sendToLobby(lobby, sender string, msg *toyohrpb.ReceivedMessage) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	delivered := 0
	for user, subs := range h.byUser {
		if user == sender {
			continue
		}
		for _, sub := range subs {
			if sub.lobby != lobby {
				continue
			}
			select {
			case sub.ch <- msg:
				delivered++
			default:
			}
		}
	}
	return delivered
}

// subscriberCount reports how many streams a player is receiving on. For the
// tests, which assert that a closed stream stops receiving -- a subscription
// that outlives its stream is a slow leak of channels nothing drains.
func (h *messageHub) subscriberCount(userID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.byUser[userID])
}

// messageKeepAlive is how often an idle message stream is reassured.
//
// The response carries a KeepAlive in its own oneof, which is the protocol
// saying outright that silence is not a valid state: a stream that goes quiet is
// treated as dead and the client stops trusting it. Thirty seconds, matching
// SubscribeMaintenanceSchedules and inside any reasonable proxy idle timeout.
const messageKeepAlive = 30 * time.Second

// RecvMessage streams the messages sent to a player.
//
// The player is the AUTHENTICATED caller, not the user the request names.
// RecvMessageRequest carries a user field; honouring it would let any console
// read another player's lobby messages.
func (l *lobbyMessagingService) RecvMessage(
	req *toyohrpb.RecvMessageRequest,
	stream grpc.ServerStreamingServer[toyohrpb.RecvMessageResponse],
) error {
	ctx := stream.Context()
	userID, ok := l.s.caller(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "")
	}

	sub := l.s.messages.subscribe(userID, req.GetLobby())
	defer l.s.messages.unsubscribe(sub)

	// One keep-alive immediately, so the client holds a stream that has spoken
	// rather than one that has only been opened.
	if err := stream.Send(messageKeepAliveResponse()); err != nil {
		return err
	}

	ticker := time.NewTicker(messageKeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// The player left the lobby. Not an error.
			return nil
		case <-ticker.C:
			if err := stream.Send(messageKeepAliveResponse()); err != nil {
				return err
			}
		case msg := <-sub.ch:
			if err := stream.Send(&toyohrpb.RecvMessageResponse{
				Payload: &toyohrpb.RecvMessageResponse_Message{
					Message: msg,
				},
			}); err != nil {
				return err
			}
		}
	}
}

// SendMessage delivers a message to named players.
//
// The sender is stamped from the token. The request's own user field is the
// client naming a sender, and a message attributed to whoever the client says
// sent it is impersonation with extra steps.
//
// The receivers are whoever the client names, and are NOT checked against a
// friendship or a session. On a private network of consoles the operator owns
// there is nobody to protect players from, and requiring friendship would break
// lobby chat between players who have not added each other — which is most of
// them. If that ever stops being true, this is the function to change.
func (l *lobbyMessagingService) SendMessage(
	ctx context.Context, req *toyohrpb.SendMessageRequest,
) (*emptypb.Empty, error) {
	userID, ok := l.s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	if len(req.GetReceiverUsers()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no receivers")
	}

	msg := l.s.receivedMessage(userID, req.GetMessageBody())
	delivered := l.s.messages.sendTo(req.GetReceiverUsers(), msg)

	// Not an error when nobody was listening. A message to a player who just
	// closed the game is a message nobody receives, and reporting that as a
	// failure would have the sender's console retry something that cannot
	// succeed.
	l.s.log.Debug("toyohr: message sent", "from", userID,
		"receivers", len(req.GetReceiverUsers()), "delivered", delivered)
	return &emptypb.Empty{}, nil
}

// SendLobbyMessage delivers a message to everybody in a lobby.
func (l *lobbyMessagingService) SendLobbyMessage(
	ctx context.Context, req *toyohrpb.SendLobbyMessageRequest,
) (*emptypb.Empty, error) {
	userID, ok := l.s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	if req.GetLobby() == "" {
		return nil, status.Error(codes.InvalidArgument, "no lobby")
	}

	msg := l.s.receivedMessage(userID, req.GetMessageBody())
	delivered := l.s.messages.sendToLobby(req.GetLobby(), userID, msg)

	l.s.log.Debug("toyohr: lobby message sent", "from", userID,
		"lobby", req.GetLobby(), "delivered", delivered)
	return &emptypb.Empty{}, nil
}

func (s *Service) receivedMessage(sender string, body *toyohrpb.MessageBody) *toyohrpb.ReceivedMessage {
	return &toyohrpb.ReceivedMessage{
		MessageBody: body,
		SenderUser:  sender,
		SendTime:    timestamppb.New(s.now()),
		// No resume token. It is what a client would present to pick up where
		// it left off, and there is nothing to resume from: messages are not
		// stored, so a token would name a position in a log that does not
		// exist. TODO(capture-needed): whether Splatoon 3 requires one to be
		// present. Change: this function, and it would mean storing messages.
	}
}

func messageKeepAliveResponse() *toyohrpb.RecvMessageResponse {
	return &toyohrpb.RecvMessageResponse{
		Payload: &toyohrpb.RecvMessageResponse_KeepAlive{
			KeepAlive: &toyohrpb.KeepAlive{},
		},
	}
}

// ---- nn.npln.toyohr.v1.UserScreening --------------------------------------

// userScreeningService is the reporting and moderation service.
type userScreeningService struct {
	toyohrpb.UnimplementedUserScreeningServer
	s *Service
}

// CreateReport accepts a player report and logs it.
//
// Accepted rather than refused, because the console treats a failure here as an
// error the player sees — and a player reporting somebody should not be shown a
// communication error.
//
// Logged rather than stored. This is a private network of consoles one person
// owns: there is no moderation queue to file it in, and the useful outcome of a
// report is that the operator hears about it. Storing reports would mean
// building a review process for a group of friends.
func (u *userScreeningService) CreateReport(
	ctx context.Context, req *toyohrpb.CreateReportRequest,
) (*toyohrpb.Report, error) {
	userID, ok := u.s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}

	report := req.GetReport()
	// Warn, not info: the whole value of this handler is that the operator
	// notices.
	u.s.log.Warn("toyohr: a player reported another player",
		"reporter", userID,
		"target", report.GetScreeningTarget(),
		"target_type", report.GetScreeningTargetType(),
		"category", report.GetCategory(),
		"reason", report.GetReason())

	out := &toyohrpb.Report{
		Name:                u.s.tenantPath() + "/reports/" + report.GetCategory(),
		Category:            report.GetCategory(),
		Reason:              report.GetReason(),
		LanguageCode:        report.GetLanguageCode(),
		ScreeningTarget:     report.GetScreeningTarget(),
		ScreeningTargetType: report.GetScreeningTargetType(),
		CreateTime:          timestamppb.New(u.s.now()),
	}
	return out, nil
}

// GetViolation reports that the caller is not in trouble.
//
// The response must carry its own resource name, ending in a "violations"
// segment. This was answered as a genuinely empty message (upstream's own
// proto marks the field list "[UNKNOWN]"), which used to be reasoned as
// unambiguous -- "no violation" and "here is your violation" are the same
// bytes when there are no fields to tell them apart. That reasoning is
// wrong: NextendoNetwork/splatoon-3 measured (facts only, not its code)
// that Splatoon 3's own client parses this response's resource name and
// hard-aborts in its resource-path parser when it is empty -- the response
// defaults to a zero-value message whose name is "". Reproduced on
// SwitchNet's own hardware on 2026-09-24: the crash's own return address
// inside Splatoon 3's own module landed a few hundred bytes from the exact
// function Nextendo's measurement names. The upstream proto has been
// updated with the confirmed `name` field (.proto-upstream's own comment
// cites this finding) and regenerated -- see internal/toyohr/README.md.
//
// SwitchNet issues no penalties: there is nobody to issue them, and a
// private server that silently banned one of the operator's own consoles
// would be a bug with no way to see it. The name always resolves to "no
// violation", never a real one.
func (u *userScreeningService) GetViolation(
	ctx context.Context, _ *toyohrpb.GetViolationRequest,
) (*toyohrpb.GetViolationResponse, error) {
	uid, ok := u.s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	return &toyohrpb.GetViolationResponse{
		Name: u.s.tenantPath() + "/users/" + uid + "/violations/current",
	}, nil
}
