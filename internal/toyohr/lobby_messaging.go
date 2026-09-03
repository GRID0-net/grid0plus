package toyohr

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/switchnet/internal/lobbymessaging"
	"github.com/n-popescu/switchnet/internal/rpcauth"
	common "github.com/n-popescu/switchnet/proto/common"
	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// lobbyMessagingKeepAlive is how often RecvMessage sends a keep-alive when
// nothing is arriving. Same interval and reasoning as
// internal/npln/messaging.go's messagingKeepAlive: RecvMessageResponse
// carries a KeepAlive message in its own oneof, the protocol itself saying
// silence is not a valid state for this stream.
const lobbyMessagingKeepAlive = 30 * time.Second

// RecvMessage streams one lobby's topic: everything retained since the
// resume token, then live as it arrives. See internal/lobbymessaging's
// package comment for why "currently subscribed to this lobby" is the whole
// of what membership means here -- there is no separate roster to consult.
func (s *Service) RecvMessage(
	req *toyohrpb.RecvMessageRequest,
	stream grpc.ServerStreamingServer[toyohrpb.RecvMessageResponse],
) error {
	ctx := stream.Context()
	if _, ok := rpcauth.From(ctx); !ok {
		return status.Error(codes.Unauthenticated, "")
	}
	lobby := lastSegment(req.GetLobby())
	if lobby == "" {
		return status.Error(codes.InvalidArgument, "no lobby named")
	}

	// Subscribe BEFORE draining, so nothing sent in the gap between the two
	// is lost -- the same ordering internal/npln/messaging.go's RecvMessage
	// uses, for the same reason.
	events, stop := s.lobbyMessages.Subscribe(lobby)
	defer stop()

	msgs, _ := s.lobbyMessages.Drain(lobby, req.GetMessageResumeToken())
	for _, m := range msgs {
		if err := stream.Send(lobbyMessageProto(m)); err != nil {
			return err
		}
	}

	ticker := time.NewTicker(lobbyMessagingKeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := stream.Send(lobbyKeepAliveProto()); err != nil {
				return err
			}
		case m, open := <-events:
			if !open {
				return nil
			}
			if err := stream.Send(lobbyMessageProto(m)); err != nil {
				return err
			}
		}
	}
}

// SendLobbyMessage broadcasts an opaque payload to everyone currently
// listening on a lobby's topic. The payload's MessageType and Fields are
// never read here, the same reason internal/messaging never reads them for
// the general Messaging service: parsing a game-private format is exactly
// what CLAUDE.md warns against for an undocumented shape.
func (s *Service) SendLobbyMessage(
	ctx context.Context, req *toyohrpb.SendLobbyMessageRequest,
) (*emptypb.Empty, error) {
	caller, ok := rpcauth.From(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	lobby := lastSegment(req.GetLobby())
	if lobby == "" {
		return nil, status.Error(codes.InvalidArgument, "no lobby named")
	}
	body := req.GetMessageBody()
	if body == nil {
		return nil, status.Error(codes.InvalidArgument, "no message_body given")
	}

	s.lobbyMessages.Send(lobby, caller.UserID, lobbymessaging.Body{
		RequestID:   body.GetMessageRequestId(),
		MessageType: body.GetMessageType(),
		Fields:      body.GetFields(),
	})
	return &emptypb.Empty{}, nil
}

// SendMessage is left to UnimplementedLobbyMessagingServer.
//
// TODO(capture-needed): SendMessageRequest addresses specific users
// (ReceiverUsers) directly and, unlike SendLobbyMessageRequest, carries no
// Lobby field at all -- nothing in the request says which lobby topic a
// receiver might be listening on. Routing it correctly would mean tracking
// which lobby each user most recently tuned into via their own RecvMessage
// call and delivering there, which is buildable but was not guessed at
// without a capture to check it against; see internal/lobbymessaging's
// README for the fuller reasoning. A capture showing a real SendMessage
// call and the RecvMessage stream it was meant to reach would settle both
// whether this reading is right and what "reach a user not currently
// listening to any lobby" is supposed to do. Change: add a SendMessage
// method here.

// lobbyMessageProto renders a broadcast message as the wire response.
func lobbyMessageProto(m lobbymessaging.Message) *toyohrpb.RecvMessageResponse {
	fields, _ := m.Body.Fields.(*common.MapValue)
	return &toyohrpb.RecvMessageResponse{
		Payload: &toyohrpb.RecvMessageResponse_Message{
			Message: &toyohrpb.ReceivedMessage{
				MessageBody: &toyohrpb.MessageBody{
					MessageRequestId: m.Body.RequestID,
					MessageType:      m.Body.MessageType,
					Fields:           fields,
				},
				SenderUser:         m.SenderID,
				MessageResumeToken: lobbymessaging.FormatToken(m.Seq),
				SendTime:           timestamppb.New(m.SentAt),
			},
		},
	}
}

func lobbyKeepAliveProto() *toyohrpb.RecvMessageResponse {
	return &toyohrpb.RecvMessageResponse{
		Payload: &toyohrpb.RecvMessageResponse_KeepAlive{
			KeepAlive: &toyohrpb.KeepAlive{IdleTimeout: durationpb.New(lobbyMessagingKeepAlive)},
		},
	}
}
