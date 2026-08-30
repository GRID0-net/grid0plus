package npln

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/switchnet/internal/messaging"
	common "github.com/n-popescu/switchnet/proto/common"
	messagingpb "github.com/n-popescu/switchnet/proto/messaging/v1"
)

// maxMessageReceivers bounds how many receivers one SendMessage call may
// fan out to -- a console (or something pretending to be one) asking for an
// unreasonable fan-out is capped rather than served without limit, the same
// shape as internal/baas's own page-size caps.
const maxMessageReceivers = 64

// messagingKeepAlive is how often RecvMessage sends a keep-alive when nothing
// is arriving.
//
// RecvMessageResponse carries a KeepAlive message in its own oneof, the
// protocol saying outright that silence is not a valid state -- the same rule
// SubscribeMaintenanceSchedules and the friends streams follow. See ROADMAP
// §2.5 and internal/npln/README.md.
const messagingKeepAlive = 30 * time.Second

// ---- nn.npln.messaging.v1.Messaging ----------------------------------------

type messagingService struct {
	messagingpb.UnimplementedMessagingServer
	s *Server
}

// SendMessage relays an opaque payload to one or more receivers.
//
// The payload's MessageType and Fields are never read here, the same reason
// internal/baas passes a game's AppField through untouched: parsing a
// game-private format is exactly what CLAUDE.md warns against inventing.
func (m *messagingService) SendMessage(
	ctx context.Context, req *messagingpb.SendMessageRequest,
) (*messagingpb.SendMessageResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	body := req.GetMessageBody()
	if body == nil || len(req.GetReceiverUsers()) == 0 {
		return nil, errMalformed(m.s.env)
	}
	if len(req.GetReceiverUsers()) > maxMessageReceivers {
		m.s.log.Warn("npln: SendMessage exceeded the receiver cap",
			"sender", caller.UserID, "receivers", len(req.GetReceiverUsers()))
		return nil, errMalformed(m.s.env)
	}

	receivers := make([]string, 0, len(req.GetReceiverUsers()))
	for _, name := range req.GetReceiverUsers() {
		if id := lastSegment(name); id != "" {
			receivers = append(receivers, id)
		}
	}

	m.s.messages.Send(caller.UserID, receivers, messaging.Body{
		RequestID:   body.GetMessageRequestId(),
		NeedAck:     body.GetNeedAck(),
		MessageType: body.GetMessageType(),
		Fields:      body.GetFields(),
	})

	return &messagingpb.SendMessageResponse{}, nil
}

// SendAck acknowledges one or more previously received messages.
//
// Routing needs nothing beyond what the client already has: AckBody carries
// the original message's sender, so the acknowledgement is delivered there
// without this server having tracked the message itself.
func (m *messagingService) SendAck(
	ctx context.Context, req *messagingpb.SendAckRequest,
) (*messagingpb.SendAckResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	for _, ack := range req.GetAcks() {
		sender := lastSegment(ack.GetSenderUser())
		if sender == "" {
			continue
		}
		m.s.messages.Ack(caller.UserID, ack.GetMessageRequestId(), sender)
	}

	return &messagingpb.SendAckResponse{}, nil
}

// RecvMessage streams the caller's mailbox: everything retained since the
// given resume tokens, then live as it arrives.
//
// TODO(capture-needed): whether an empty resume token really means "replay
// everything retained" rather than "nothing before this call counts". This
// is the invite mailbox internal/npln/README.md's boot-order notes describe
// -- called early, before a session needs anything else -- and "replay
// everything" is the defensible reading: a pending invite sent while the
// console was off has to still be there when it connects, or the mailbox is
// worthless for the one thing it exists for. Getting it backwards either
// loses an invite silently or replays a stale one on every reconnect. A
// capture of a console reconnecting with an unread invite pending would
// settle it. Change: this function's Drain call.
func (m *messagingService) RecvMessage(
	req *messagingpb.RecvMessageRequest,
	stream grpc.ServerStreamingServer[messagingpb.RecvMessageResponse],
) error {
	ctx := stream.Context()
	caller, ok := CallerFrom(ctx)
	if !ok {
		return ErrUnauthenticated
	}

	// Subscribe BEFORE draining, so nothing sent in between is lost: an
	// arrival between Drain reading the mailbox and Subscribe attaching would
	// otherwise reach neither.
	events, stop := m.s.messages.Subscribe(caller.UserID)
	defer stop()

	msgs, acks, _, _ := m.s.messages.Drain(caller.UserID,
		req.GetMessageResumeToken(), req.GetAckResumeToken())
	for _, mm := range msgs {
		if err := stream.Send(recvMessageProto(mm)); err != nil {
			return err
		}
	}
	for _, a := range acks {
		if err := stream.Send(recvAckProto(a)); err != nil {
			return err
		}
	}

	ticker := time.NewTicker(messagingKeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := stream.Send(messagingKeepAliveProto()); err != nil {
				return err
			}
		case e, open := <-events:
			if !open {
				return nil
			}
			var err error
			switch v := e.(type) {
			case messaging.Message:
				err = stream.Send(recvMessageProto(v))
			case messaging.Ack:
				err = stream.Send(recvAckProto(v))
			}
			if err != nil {
				return err
			}
		}
	}
}

func recvMessageProto(m messaging.Message) *messagingpb.RecvMessageResponse {
	fields, _ := m.Body.Fields.(*common.MapValue)
	return &messagingpb.RecvMessageResponse{
		Reply: &messagingpb.RecvMessageResponse_Message{
			Message: &messagingpb.RecvMessage{
				MessageBody: &messagingpb.MessageBody{
					MessageRequestId: m.Body.RequestID,
					NeedAck:          m.Body.NeedAck,
					MessageType:      m.Body.MessageType,
					Fields:           fields,
				},
				SenderUser:         userResourceName(m.SenderID),
				MessageResumeToken: messaging.FormatToken(m.Seq),
				SendTime:           timestamppb.New(m.SentAt),
			},
		},
	}
}

func recvAckProto(a messaging.Ack) *messagingpb.RecvMessageResponse {
	return &messagingpb.RecvMessageResponse{
		Reply: &messagingpb.RecvMessageResponse_Ack{
			Ack: &messagingpb.RecvAck{
				Ack: &messagingpb.AckBody{
					MessageRequestId: a.RequestID,
					SenderUser:       userResourceName(a.OriginalSender),
				},
				ReceiverUser:   userResourceName(a.AckedByID),
				AckResumeToken: messaging.FormatToken(a.Seq),
				AckSendTime:    timestamppb.New(a.SentAt),
			},
		},
	}
}

func messagingKeepAliveProto() *messagingpb.RecvMessageResponse {
	return &messagingpb.RecvMessageResponse{
		Reply: &messagingpb.RecvMessageResponse_KeepAlive{
			KeepAlive: &messagingpb.KeepAlive{IdleTimeout: durationpb.New(messagingKeepAlive)},
		},
	}
}
