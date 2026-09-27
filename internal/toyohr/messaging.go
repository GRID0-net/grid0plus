package toyohr

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
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
// treated as dead and the client stops trusting it. Both working captures
// send a keepalive every 20 seconds.
const messageKeepAlive = 20 * time.Second

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
	if err := stream.Send(messageKeepAliveResponse(l.s.now())); err != nil {
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
			if err := stream.Send(messageKeepAliveResponse(l.s.now())); err != nil {
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

	receivers := make([]string, 0, len(req.GetReceiverUsers()))
	for _, name := range req.GetReceiverUsers() {
		id, ok := l.s.messageReceiverID(name)
		if !ok {
			return nil, status.Error(codes.InvalidArgument, "invalid receiver user")
		}
		receivers = append(receivers, id)
	}
	msg := l.s.receivedMessage(userID, req.GetMessageBody())
	delivered := l.s.messages.sendTo(receivers, msg)

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
		MessageBody:        body,
		SenderUser:         s.tenantPath() + "/users/" + sender,
		SendTime:           timestamppb.New(s.now()),
		MessageResumeToken: lobbyResumeToken(s.now()),
	}
}

// The console names recipients as NPLN resources, while the message hub is
// keyed by authenticated bare user IDs. Preserve that boundary so a real
// self-addressed NplnLogin reaches the caller's RecvMessage subscription.
func (s *Service) messageReceiverID(name string) (string, bool) {
	for _, prefix := range []string{"tenants/current/users/", s.tenantPath() + "/users/", "users/"} {
		if strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
			break
		}
	}
	return name, name != "" && !strings.Contains(name, "/")
}

// lobbyResumeToken renders the cursor shape both preserved working Nextendo
// hall captures use verbatim on every KeepAlive and ReceivedMessage on this
// stream (docs/CAPTURES/raw/nextendo-hall-20260923T205738Z,
// nextendo-second-20260923T210513Z): {"v":1,"t":"<unix ms>-0"}. Settles the
// TODO(capture-needed) this function used to carry -- whether Splatoon 3
// requires a resume token to be present at all -- with a real answer: yes,
// non-empty, in both captures, on every frame. The trailing "-0" is a
// sequence position within that millisecond; this stream never buffers more
// than one frame per millisecond, so 0 is always correct, not a guess about
// an unobserved case.
//
// It is still opaque and unresumable, same as before this fix: RecvMessage
// does not accept a resume token on this stream (there is nothing stored to
// resume from -- see lobbyMessagingService.RecvMessage), so this only
// answers the shape the client is reading, not a real position in a log.
func lobbyResumeToken(now time.Time) string {
	return fmt.Sprintf(`{"v":1,"t":"%d-0"}`, now.UnixMilli())
}

func messageKeepAliveResponse(now time.Time) *toyohrpb.RecvMessageResponse {
	return &toyohrpb.RecvMessageResponse{
		Payload: &toyohrpb.RecvMessageResponse_KeepAlive{
			KeepAlive: &toyohrpb.KeepAlive{
				IdleTimeout:        durationpb.New(messageKeepAlive),
				MessageResumeToken: lobbyResumeToken(now),
			},
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

// GetViolation returns an absent resource for a caller with no sanction.
// A successful response with a name means the violation EXISTS; the former
// synthetic /violations/current answer could block a fresh player from the
// hall. A successful response without a message makes the game's parser see
// an empty resource name and can crash it. Nextendo's measured working path
// uses NotFound with response headers sent before the status trailers by
// the NPLN unary interceptor.
func (u *userScreeningService) GetViolation(
	ctx context.Context, _ *toyohrpb.GetViolationRequest,
) (*toyohrpb.GetViolationResponse, error) {
	if _, ok := u.s.caller(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	return nil, status.Error(codes.NotFound, "violation not found")
}
