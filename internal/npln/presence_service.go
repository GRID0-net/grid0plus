package npln

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/presence"
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
var presenceKeepAlive = 30 * time.Second

// Captured SubscribePresences sends every 20s while advertising a 30s interval.
var presenceStreamTick = 20 * time.Second

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

// presenceHeartbeatDeadline is field 2 of a real Heartbeat message -- a
// nested {1: 50} carrying the 50-second deadline the client actually uses,
// alongside the documented field 1 (Interval, 30s). Neither this repository's
// nor NextendoNetwork/splatoon-3's reconstructed .proto names field 2 (it is
// genuinely undocumented -- switchbrew and kinnay's wiki have nothing for
// it), but its raw bytes are a directly observable fact of the wire, not a
// guess: NextendoNetwork/splatoon-3 records capturing exactly
// `{2: {1: 50}}` from a real Nintendo server (facts only, its code not used
// here -- this is reconstructed independently against the same confirmed
// byte sequence, protoreflect.RawFields([]byte{0x12, 0x02, 0x08, 0x32})).
// Leaving it off is a likely reason a client would apply a shorter, internal
// default deadline instead of the stated one.
var presenceHeartbeatDeadline = protoreflect.RawFields([]byte{0x12, 0x02, 0x08, 0x32})

// presenceHeartbeat builds the Heartbeat both KeepAlive and SubscribePresences
// send, carrying the confirmed {30s interval, 50s deadline} pair.
func presenceHeartbeat() *friendspb.Heartbeat {
	hb := &friendspb.Heartbeat{Interval: durationpb.New(presenceKeepAlive)}
	hb.ProtoReflect().SetUnknown(presenceHeartbeatDeadline)
	return hb
}

// KeepAlive is a bidirectional liveness stream.
//
// Answers every client ping with an immediate ack, on a real Nintendo
// server's own confirmed behaviour: NextendoNetwork/splatoon-3 (facts only,
// not its code) measured a real console sending 128, 34 and 77 pings across
// three sessions and getting exactly one reply per ping, each. Their first
// attempt at this RPC instead answered on its own independent 30s timer,
// unrelated to the client's pings -- reasoning that acking immediately would
// cause a runaway feedback loop -- and that was wrong: real KeepAlive
// streams died at 59.998s / 1m0.001s with "a communication error has
// occurred", because the FIRST reply is what announces the {30s, 50s}
// contract (see presenceHeartbeat) the client times its own patience
// against, and answering only every 30s left it waiting past its own
// deadline for a reply to its very first ping.
//
// The first reply is sent immediately, before any ping has even arrived, for
// the same reason: it is the one that sets the client's clock. Every
// subsequent client message (ping or UpdatePresence) gets its own paced ack.
// A ticker independently sends one on presenceStreamTick even if the
// client falls silent, so the stream does not go quiet if the client's own
// pings ever stop.
//
// TODO(capture-needed): whether Splatoon 3 ever sends UpdatePresence over
// this RPC to report richer, game-specific state (KeepAliveRequest carries a
// Presence + FieldMask for exactly that). SwitchNet does not persist it: the
// single presence store BAAS's PATCH device_accounts endpoint already owns is
// deliberately not given a second writer here, for the same reason
// docs/ROADMAP.md#2.5 insists on one friend graph rather than two -- a second
// presence writer that disagrees with the first is a worse bug than an
// unread field.
// Change: this function, if a capture shows attributes the client actually
// depends on.
// presenceKeepAliveAckMinInterval paces replies without dropping any ping.
// A 2026-09-24 SwitchNet capture saw 101 ping/ack round trips in two seconds
// (~50Hz), followed by a hall communication error. Dropping pings during a
// one-second throttle was also wrong: the client waits for the reply before
// sending its next ping, so it got only the opening response and abandoned
// the stream before the next 20-second tick. Both working Nextendo captures
// show essentially one reply per ping, about 20 per second. A 50ms floor
// preserves that exchange while bounding the feedback loop.
const presenceKeepAliveAckMinInterval = 50 * time.Millisecond

func (p *presenceService) KeepAlive(
	stream grpc.BidiStreamingServer[friendspb.KeepAliveRequest, friendspb.KeepAliveResponse],
) error {
	ctx := stream.Context()
	caller, ok := CallerFrom(ctx)
	if !ok {
		return ErrUnauthenticated
	}

	// A gRPC stream does not allow two concurrent Sends: the drain loop below
	// and this function's own ticker both go through send().
	//
	// lastSent is real wall-clock time (time.Now), not p.s.now() -- the same
	// choice the ticker below already makes. p.s.now() is a mockable
	// business clock tests can freeze; the throttle it would gate is
	// inherently about real round-trip speed, and freezing it would make
	// every ping after the first look simultaneous forever.
	var sendMu sync.Mutex
	var lastSent time.Time
	send := func() error {
		sendMu.Lock()
		defer sendMu.Unlock()
		if wait := presenceKeepAliveAckMinInterval - time.Since(lastSent); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
		lastSent = time.Now()
		return stream.Send(&friendspb.KeepAliveResponse{Heartbeat: presenceHeartbeat()})
	}

	sendErr := make(chan error, 1)
	recvErr := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				recvErr <- err
				return
			}
			if err := p.s.store.TouchPresence(ctx, caller.UserID, p.s.now()); err != nil {
				// Liveness is a nicety, same as internal/baas's own
				// TouchPresence call: failing the stream over it would turn a
				// cosmetic problem into a dropped connection.
				p.s.log.Warn("npln: touch presence", "user", caller.UserID, "error", err)
			}
			if err := send(); err != nil {
				sendErr <- err
				return
			}
		}
	}()

	// The opening reply: sent before any ping has arrived, because it is the
	// one that announces the contract the client calibrates its own patience
	// against.
	if err := send(); err != nil {
		return err
	}

	ticker := time.NewTicker(presenceStreamTick)
	defer ticker.Stop()

	for {
		select {
		case err := <-sendErr:
			return err
		case err := <-recvErr:
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := send(); err != nil {
				return err
			}
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

	// Both working captures send the heartbeat contract before the initial
	// presence page. The game can then distinguish an open stream from a
	// silent one while the page is still being assembled.
	if err := stream.Send(&friendspb.SubscribePresencesResponse{
		Response: &friendspb.SubscribePresencesResponse_Heartbeat{
			Heartbeat: presenceHeartbeat(),
		},
	}); err != nil {
		return err
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

	ticker := time.NewTicker(presenceStreamTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := stream.Send(&friendspb.SubscribePresencesResponse{
				Response: &friendspb.SubscribePresencesResponse_Heartbeat{
					Heartbeat: presenceHeartbeat(),
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
// ids, dropping anything the caller may not watch: itself, always, and a
// friend only when the friend's own presence.Permissions setting allows this
// caller to see it -- the same check internal/npln/friends.go's
// FriendUser.Relationship.PresenceReceivable already makes for the friends
// list stream (see presence.Visible's own doc comment). Without it here, a
// console could subscribe directly to a friend's presence resource and see
// real ONLINE/PLAYING state and title regardless of that friend having set
// their presence to SELF or FAVORITE_FRIENDS-while-not-favourited -- exactly
// what PresenceReceivable tells the client it should not be shown.
//
// Capped at maxSubscribedPresences.
func (s *Server) watchablePresenceIDs(ctx context.Context, callerID string, names []string) ([]string, error) {
	friendships, err := s.store.ListFriends(ctx, callerID, maxSubscribedPresences)
	if err != nil {
		return nil, err
	}
	friend := make(map[string]bool, len(friendships))
	for _, fr := range friendships {
		friend[fr.FriendID] = true
	}

	out := make([]string, 0, min(len(names), maxSubscribedPresences))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		id := presenceUserID(name)
		if id == "" || seen[id] {
			continue
		}
		if id != callerID {
			if !friend[id] {
				continue
			}
			owner, err := s.store.GetUser(ctx, id)
			// Fails closed, the same as presence.Visible's own unrecognised-
			// setting branch: a lookup error is not evidence the friend
			// wants their presence shown.
			if err != nil || !presence.Visible(ctx, s.store, owner, callerID) {
				continue
			}
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
