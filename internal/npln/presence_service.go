package npln

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/presencebus"
	friendspb "github.com/n-popescu/switchnet/proto/friends/v1"
)

// presenceKeepAlive is how often SubscribePresences sends a heartbeat when
// nothing being watched has changed, and the interval KeepAlive reports in
// every response.
//
// Unlike SubscribeFriendUsers, this stream's protocol has a real
// heartbeat-only variant (Heartbeat, in the response oneof), so keeping it
// warm does not require resending the presences themselves.
const presenceKeepAlive = 30 * time.Second

// maxSubscribedPresences bounds how many presence names one SubscribePresences
// call may watch, the same shape as internal/baas's page-size caps: a console
// asking for an unreasonable number is capped rather than iterated without
// limit.
const maxSubscribedPresences = 1000

// ---- nn.npln.friends.v1.PresenceService ------------------------------------

type presenceService struct {
	friendspb.UnimplementedPresenceServiceServer
	s *Server
}

// KeepAlive is a bidirectional liveness stream.
//
// TODO(capture-needed): whether Splatoon 3 ever sends UpdatePresence over
// this RPC to report richer, game-specific state (KeepAliveRequest carries a
// Presence + FieldMask for exactly that). SwitchNet does not persist it: the
// single presence store BAAS's PATCH device_accounts endpoint already owns is
// deliberately not given a second writer here, for the same reason
// docs/ROADMAP.md#2.5 insists on one friend graph rather than two -- a second
// presence writer that disagrees with the first is a worse bug than an
// unread field. What every message on this stream DOES do, Ack or
// UpdatePresence alike, is count as proof of life: the caller is talking to
// NPLN, so internal/presence's liveness rule applies exactly as it does for
// an authenticated BAAS request (see enter() in server.go, which already
// touches presence once per call -- this stream just keeps doing it for as
// long as it stays open).
// Change: this function, if a capture shows attributes the client actually
// depends on.
func (p *presenceService) KeepAlive(
	stream grpc.BidiStreamingServer[friendspb.KeepAliveRequest, friendspb.KeepAliveResponse],
) error {
	ctx := stream.Context()
	caller, ok := CallerFrom(ctx)
	if !ok {
		return ErrUnauthenticated
	}

	for {
		if _, err := stream.Recv(); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}

		if err := p.s.store.TouchPresence(ctx, caller.UserID, p.s.now()); err != nil {
			// Liveness is a nicety, same as internal/baas's own TouchPresence
			// call: failing the stream over it would turn a cosmetic problem
			// into a dropped connection.
			p.s.log.Warn("npln: touch presence", "user", caller.UserID, "error", err)
		}

		if err := stream.Send(&friendspb.KeepAliveResponse{
			Heartbeat: &friendspb.Heartbeat{Interval: durationpb.New(presenceKeepAlive)},
		}); err != nil {
			return err
		}
	}
}

// SubscribePresences streams the presence of the users the caller asked
// about, pushing a change as soon as internal/presencebus reports one and
// heartbeating otherwise.
//
// Only the caller's own presence and their friends' are servable: anyone else
// named is silently dropped from the result rather than refusing the whole
// call, the same reasoning internal/baas/friends.go's listFriends uses for a
// friendship naming a deleted user -- a console that asked about a stranger
// alongside real friends should still get the friends.
func (p *presenceService) SubscribePresences(
	req *friendspb.SubscribePresencesRequest,
	stream grpc.ServerStreamingServer[friendspb.SubscribePresencesResponse],
) error {
	ctx := stream.Context()
	caller, ok := CallerFrom(ctx)
	if !ok {
		return ErrUnauthenticated
	}

	watched, err := p.s.watchablePresenceIDs(ctx, caller.UserID, req.GetPresences())
	if err != nil {
		p.s.log.Error("npln: resolve watched presences", "user", caller.UserID, "error", err)
		return errInternal()
	}

	send := func() error {
		presences, err := p.s.presence.EffectiveMany(ctx, watched, p.s.now())
		if err != nil {
			p.s.log.Error("npln: load presences", "user", caller.UserID, "error", err)
			return errInternal()
		}
		out := make([]*friendspb.Presence, 0, len(watched))
		for _, id := range watched {
			out = append(out, p.s.presenceProto(id, presences[id]))
		}
		if err := stream.Send(&friendspb.SubscribePresencesResponse{
			Response: &friendspb.SubscribePresencesResponse_Presences_{
				Presences: &friendspb.SubscribePresencesResponse_Presences{Presences: out},
			},
		}); err != nil {
			return err
		}
		return stream.Send(&friendspb.SubscribePresencesResponse{
			Response: &friendspb.SubscribePresencesResponse_EnumerationDone{
				EnumerationDone: &friendspb.SubscribePresencesResponse_PresenceEnumerationDone{},
			},
		})
	}

	if err := send(); err != nil {
		return err
	}

	var events <-chan presencebus.Event
	if p.s.bus != nil {
		ch, err := p.s.bus.Subscribe(ctx)
		if err != nil {
			p.s.log.Error("npln: subscribe presence bus", "error", err)
		} else {
			events = ch
		}
	}

	watchedSet := make(map[string]bool, len(watched))
	for _, id := range watched {
		watchedSet[id] = true
	}

	ticker := time.NewTicker(presenceKeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := stream.Send(&friendspb.SubscribePresencesResponse{
				Response: &friendspb.SubscribePresencesResponse_Heartbeat{
					Heartbeat: &friendspb.Heartbeat{Interval: durationpb.New(presenceKeepAlive)},
				},
			}); err != nil {
				return err
			}
		case e, open := <-events:
			if !open {
				events = nil
				continue
			}
			if !watchedSet[e.UserID] {
				continue
			}
			presences, err := p.s.presence.EffectiveMany(ctx, []string{e.UserID}, p.s.now())
			if err != nil {
				p.s.log.Error("npln: load presence", "user", e.UserID, "error", err)
				return errInternal()
			}
			if err := stream.Send(&friendspb.SubscribePresencesResponse{
				Response: &friendspb.SubscribePresencesResponse_Presences_{
					Presences: &friendspb.SubscribePresencesResponse_Presences{
						Presences: []*friendspb.Presence{p.s.presenceProto(e.UserID, presences[e.UserID])},
					},
				},
			}); err != nil {
				return err
			}
		}
	}
}

// watchablePresenceIDs resolves the requested presence resource names to user
// ids, dropping anything the caller may not watch: itself and its friends
// only, capped at maxSubscribedPresences.
func (s *Server) watchablePresenceIDs(ctx context.Context, callerID string, names []string) ([]string, error) {
	friendships, err := s.store.ListFriends(ctx, callerID, maxSubscribedPresences)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(friendships)+1)
	allowed[callerID] = true
	for _, fr := range friendships {
		allowed[fr.FriendID] = true
	}

	out := make([]string, 0, min(len(names), maxSubscribedPresences))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		id := presenceUserID(name)
		if id == "" || !allowed[id] || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) >= maxSubscribedPresences {
			break
		}
	}
	return out, nil
}

// presenceUserID takes the user id out of a presence resource name.
//
// TODO(capture-needed): the exact shape (see presenceProto's Name field for
// what this server emits). Lenient on purpose, same reasoning as
// matchmaking.go's lastSegment: this trims a trailing "/presence" singleton
// segment if present, then takes whatever is left after the last "/", so a
// wrong prefix costs a cosmetic mismatch rather than every subscription
// coming back empty.
func presenceUserID(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), "/")
	name = strings.TrimSuffix(name, "/presence")
	return lastSegment(name)
}

// presenceProto renders one user's resolved presence as the wire message.
//
// TODO(capture-needed): Attributes is left empty. resources.proto carries a
// map<string, nn.npln.common.Value> here, presumably where a title's
// in-game detail (SwitchNet's own AppField, in BAAS terms) would go, but
// nothing confirms the keys Splatoon 3 reads. Inventing one is exactly what
// CLAUDE.md warns against for an undocumented shape: an empty map is
// wrong only in the sense of "incomplete", where a guessed key risks being
// wrong in the sense of "silently never matches". Change: this function.
func (s *Server) presenceProto(userID string, p db.Presence) *friendspb.Presence {
	return &friendspb.Presence{
		Name:  s.tenantPath() + "/users/" + userID + "/presence",
		State: presenceState(p.State),
		LastOnlineTime: func() *timestamppb.Timestamp {
			if p.LastSeenAt.IsZero() {
				return nil
			}
			return timestamppb.New(p.LastSeenAt)
		}(),
	}
}

// presenceState maps SwitchNet's four presence states onto NPLN's own State
// enum, which has no PLAYING equivalent -- resources.proto only defines
// ONLINE, OFFLINE and INACTIVE. PLAYING is treated as ONLINE: a player in a
// game is, at minimum, online, and BAAS's own richer detail (title id, app
// field) is what the friends applet reads separately rather than through
// this service.
func presenceState(state string) friendspb.State {
	switch state {
	case db.StatePlaying, db.StateOnline:
		return friendspb.State_ONLINE
	case db.StateInactive:
		return friendspb.State_INACTIVE
	default:
		return friendspb.State_OFFLINE
	}
}
