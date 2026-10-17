package npln

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/grid0plus/internal/matchmaking"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	mmpb "github.com/n-popescu/grid0plus/proto/matchmaking/v1"
)

// Private rooms: a private battle, a Salmon Run room, or a squad a host opens
// for friends to join. Splatoon 3 opens one with
// GameSessionService.CreateGameSessionCreationTicket instead of matchmaking,
// and every one of these RPCs used to answer Unimplemented, which the game
// shows as 2321-4224 the moment a player presses "create room".
//
// The shapes below come from a real Nintendo capture of this exact flow that
// NextendoNetwork/splatoon-3 records in its own comments (facts only,
// PolyForm Shield -- none of its code is used here). They are second-hand:
// their capture, not ours, so docs/CAPTURES/README.md lists what a GRID0+
// capture of a room being opened and joined would confirm.
//
// A room is a matchmaking.Session with its host as the only player, so every
// call downstream of creation -- GetGameSession, JoinGameSession, the gamesync
// channel and its session token -- is the one matched sessions already use.

// roomTicket is one GameSessionCreationTicket.
type roomTicket struct {
	id        string
	sessionID string
	userID    string
	config    string
	// hostDef is the host's UserDefinition as the request sent it, with the
	// user replaced by the concrete one.
	hostDef *mmpb.UserDefinition
	// pendingProps are the properties the request carried, echoed on the
	// PENDING answer.
	pendingProps *commonpb.MapValue
}

// roomMeta is what a room carries beyond a matchmaking.Session.
type roomMeta struct {
	config     string
	properties *commonpb.MapValue
	password   string
	alias      string
}

type rooms struct {
	mu      sync.Mutex
	tickets map[string]*roomTicket
	meta    map[string]*roomMeta
	// aliases maps a room code to its session id.
	aliases map[string]string
}

func newRooms() *rooms {
	return &rooms{
		tickets: map[string]*roomTicket{},
		meta:    map[string]*roomMeta{},
		aliases: map[string]string{},
	}
}

// forget drops everything that names a room: its tickets, metadata and code.
func (r *rooms) forget(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, t := range r.tickets {
		if t.sessionID == sessionID {
			delete(r.tickets, id)
		}
	}
	if m := r.meta[sessionID]; m != nil && m.alias != "" {
		delete(r.aliases, m.alias)
	}
	delete(r.meta, sessionID)
}

// sweep drops the records of rooms the queue has already expired. Called on
// every room creation so the maps are bounded by the live rooms rather than
// growing for the life of the process.
func (r *rooms) sweep(alive func(sessionID string) bool) {
	r.mu.Lock()
	var dead []string
	for id := range r.meta {
		if !alive(id) {
			dead = append(dead, id)
		}
	}
	r.mu.Unlock()
	for _, id := range dead {
		r.forget(id)
	}
}

// roomCapacity is how many players a room in this mode takes.
//
// The request carries no size of its own, so the server sets it. Salmon Run's
// 4 is in the capture. Private battle's 10 (eight players and two spectators)
// is NextendoNetwork's reading, not a captured value. Every other mode the
// game has opened on grid0 (2026-09-27 to 10-13: private_match_config,
// coop_private_config and join_room_4_no_referee_config, the squad) names
// its own size as join_room_<n>, so that number is used.
func roomCapacity(config string) int {
	if n, ok := joinRoomSize(config); ok {
		return n
	}
	switch {
	case strings.Contains(config, "coop"):
		return 4
	case strings.Contains(config, "private_match"):
		return 10
	default:
		return 4
	}
}

// joinRoomSize reads n from a "join_room_<n>_..." config name.
func joinRoomSize(config string) (int, bool) {
	_, rest, ok := strings.Cut(config, "join_room_")
	if !ok {
		return 0, false
	}
	digits, _, _ := strings.Cut(rest, "_")
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 || n > 10 {
		return 0, false
	}
	return n, true
}

// roomProperties keeps every property the client sent and adds the two the
// server owns. Merged, not replaced: replacing the map dropped the client's
// own game_mode and userName, and the game abandoned the room with
// CreateSessionFailed and an empty session id.
func roomProperties(config string, requested *commonpb.MapValue, alias string) *commonpb.MapValue {
	out := &commonpb.MapValue{Fields: map[string]*commonpb.Value{}}
	for k, v := range requested.GetFields() {
		if k == "_BaseConfigName" || k == "_AliasSuffix" {
			continue
		}
		out.Fields[k] = proto.Clone(v).(*commonpb.Value)
	}
	out.Fields["_BaseConfigName"] = stringValue(lastSegment(config))
	// _AliasSuffix carries the room code once CreateGameSessionShortAlias has
	// assigned one; the game shows it as the Room ID.
	out.Fields["_AliasSuffix"] = stringValue(alias)
	return out
}

func stringValue(s string) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: s}}
}

// configName resolves a requested matchmaking config to the concrete tenant.
// The game sends "tenants/current/matchmakingConfigs/<name>" and a real server
// answers "tenants/<tenant>/matchmakingConfigs/<name>".
func (s *Server) configName(config string) string {
	if config == "" {
		return ""
	}
	return s.tenantPath() + "/matchmakingConfigs/" + lastSegment(config)
}

// concreteUserDefinition is the caller's own UserDefinition with the user
// replaced by the authenticated one. The attributes, latencies and team are
// the client's to report; who it is, is not. The concrete name also matters to
// the game itself: given the "current" alias back, the host cannot find itself
// among the room's participants.
func (s *Server) concreteUserDefinition(userID string, requested []*mmpb.UserDefinition) *mmpb.UserDefinition {
	ud := &mmpb.UserDefinition{User: s.userResourceName(userID)}
	if len(requested) > 0 {
		ud.Attributes = requested[0].GetAttributes()
		ud.LatencyData = requested[0].GetLatencyData()
		ud.Team = requested[0].GetTeam()
	}
	return ud
}

// CreateGameSessionCreationTicket opens a private room with the caller as
// host.
//
// The request carries the matchmaking config and the host's UserDefinition.
// The answer is PENDING; the room itself arrives on
// TrackGameSessionCreationTicket, the same split a real server makes.
func (g *gameSessionService) CreateGameSessionCreationTicket(
	ctx context.Context, req *mmpb.CreateGameSessionCreationTicketRequest,
) (*mmpb.GameSessionCreationTicket, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}
	in := req.GetGameSessionCreationTicket()
	config := in.GetMatchmakingConfig()
	if config == "" {
		// A room with no mode is a room the game cannot start. Refused rather
		// than guessed, the same as a matchmaking ticket with no config.
		g.s.log.Debug("npln: room creation with no config", "user", caller.UserID)
		return nil, errMalformed(g.s.env)
	}
	base := lastSegment(config)
	if _, seen := g.s.roomModesSeen.LoadOrStore(base, true); !seen {
		g.s.log.Info("npln: first room opened in this mode",
			"config", base, "capacity", roomCapacity(base))
	}

	g.s.rooms.sweep(func(id string) bool { _, ok := g.s.queue.Session(id); return ok })

	sess, err := g.s.queue.OpenRoom(caller.UserID, base, roomCapacity(base))
	if err != nil {
		g.s.log.Error("npln: open room", "user", caller.UserID, "error", err)
		return nil, errInternal()
	}

	requestedProps := in.GetGameSession().GetProperties()
	ticket := &roomTicket{
		id:           uuid.NewString(),
		sessionID:    sess.ID,
		userID:       caller.UserID,
		config:       config,
		hostDef:      g.s.concreteUserDefinition(caller.UserID, in.GetUserDefinitions()),
		pendingProps: requestedProps,
	}
	g.s.rooms.mu.Lock()
	g.s.rooms.tickets[ticket.id] = ticket
	g.s.rooms.meta[sess.ID] = &roomMeta{
		config:     base,
		properties: roomProperties(base, requestedProps, ""),
		password:   in.GetGameSession().GetPassword(),
	}
	g.s.rooms.mu.Unlock()

	g.s.log.Info("npln: room opened",
		"session", sess.ID, "config", base, "host", caller.UserID, "capacity", sess.MaxPlayers)

	out := &mmpb.GameSessionCreationTicket{
		Name:              g.s.roomTicketName(ticket.id),
		MatchmakingConfig: g.s.configName(config),
		UserDefinitions:   []*mmpb.UserDefinition{ticket.hostDef},
		State:             mmpb.GameSessionCreationTicket_PENDING,
	}
	// A real server's PENDING answer already carries a GameSession holding
	// only the properties the request sent.
	if requestedProps != nil {
		out.GameSession = &mmpb.GameSession{Properties: requestedProps}
	}
	return out, nil
}

func (s *Server) roomTicketName(id string) string {
	return s.tenantPath() + "/gameSessionCreationTickets/" + id
}

// TrackGameSessionCreationTicket sends the room, once, already SUCCEEDED, and
// then closes the stream.
//
// Both halves are measured on real hardware and both are the opposite of what
// TrackMatchmakingTicket does, which is why this is not built on it. The
// captured stream carries a single message already at SUCCEEDED, not PENDING
// then SUCCEEDED. And the close is what tells the game creation is done: held
// open, the game waited about twenty seconds and then cancelled the ticket;
// closed, it dialled its session server within a second.
// TrackMatchmakingTicket stays open after SUCCEEDED for a reason specific to
// matched tickets (see its own comment). Do not make these two agree.
func (g *gameSessionService) TrackGameSessionCreationTicket(
	req *mmpb.TrackGameSessionCreationTicketRequest,
	stream grpc.ServerStreamingServer[mmpb.GameSessionCreationTicket],
) error {
	caller, ok := CallerFrom(stream.Context())
	if !ok {
		return ErrUnauthenticated
	}
	t, found := g.s.ownRoomTicket(lastSegment(req.GetName()), caller.UserID)
	if !found {
		return ErrNotFound
	}
	out, err := g.s.succeededRoomTicket(t)
	if err != nil {
		return err
	}
	return stream.Send(out)
}

// ownRoomTicket looks a ticket up for its owner. Somebody else's ticket is
// NotFound rather than PermissionDenied, the same rule TrackMatchmakingTicket
// applies: whether a given ticket exists is not this caller's to learn.
func (s *Server) ownRoomTicket(id, userID string) (roomTicket, bool) {
	s.rooms.mu.Lock()
	defer s.rooms.mu.Unlock()
	t := s.rooms.tickets[id]
	if t == nil || t.userID != userID {
		return roomTicket{}, false
	}
	return *t, true
}

// succeededRoomTicket renders a room ticket at SUCCEEDED: the room, and the
// host's own matched user session carrying the session token its console
// opens gamesync with -- the same token a matched player gets, or the host
// dials the session server and never sends a request (2321-4992).
func (s *Server) succeededRoomTicket(t roomTicket) (*mmpb.GameSessionCreationTicket, error) {
	sess, ok := s.queue.Session(t.sessionID)
	if !ok {
		return nil, ErrNotFound
	}
	token, err := s.issueSessionTokenForDefinition(t.userID, sess.ID, t.hostDef)
	if err != nil {
		return nil, err
	}
	room := s.sessionProto(sess)
	// The captured ticket's GameSession has no user_sessions; GetGameSession
	// is where a real server lists them.
	room.UserSessions = nil
	return &mmpb.GameSessionCreationTicket{
		Name:              s.roomTicketName(t.id),
		MatchmakingConfig: s.configName(t.config),
		UserDefinitions:   []*mmpb.UserDefinition{t.hostDef},
		State:             mmpb.GameSessionCreationTicket_SUCCEEDED,
		GameSession:       room,
		MatchedUserSessions: []*mmpb.MatchedUserSession{{
			UserDefinition:     t.hostDef,
			UserSession:        s.userSessionName(sess.ID, t.userID),
			MatchmakingIdToken: token,
		}},
	}, nil
}

// CancelGameSessionCreationTicket closes the host's room, and everything that
// names it. Removing only the ticket would leave the room behind, with its
// host still counted inside and its code still resolving.
func (g *gameSessionService) CancelGameSessionCreationTicket(
	ctx context.Context, req *mmpb.CancelGameSessionCreationTicketRequest,
) (*emptypb.Empty, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}
	t, found := g.s.ownRoomTicket(lastSegment(req.GetName()), caller.UserID)
	if !found {
		return nil, ErrNotFound
	}
	g.s.queue.CloseRoom(t.sessionID)
	g.s.rooms.forget(t.sessionID)
	g.s.log.Info("npln: room closed", "session", t.sessionID, "host", caller.UserID)
	return &emptypb.Empty{}, nil
}

// GamesyncMemberGone takes a player out of a game session once gamesync has
// seen them go: see internal/gamesync/departures.go for when that is.
//
// Every session, not only rooms. A matched session outlives its first match
// whenever the group presses "Again!", and the consoles backfilling it are
// handed its players exactly as a room's joiners are.
//
// A room that empties is closed, as its host cancelling it would. A host
// leaving a room others are still in is only removed like anyone else:
// whether the game moves the room to another console or ends it is not
// something GRID0+ has seen, and closing it under the players still there
// would be a guess with a worse failure than the one being fixed.
func (s *Server) GamesyncMemberGone(sessionID, userID string) {
	sess, ok := s.queue.Leave(sessionID, userID)
	if !ok {
		return
	}
	s.log.Info("npln: player left the session", "session", sessionID, "user", userID,
		"room", sess.Room, "host", sess.Host == userID, "players", len(sess.Players))
	if sess.Room && len(sess.Players) == 0 && s.queue.CloseRoom(sessionID) {
		s.rooms.forget(sessionID)
		s.log.Info("npln: room closed; everyone left", "session", sessionID)
	}
}

// roomMetaFor returns a copy of a room's metadata.
func (s *Server) roomMetaFor(sessionID string) (roomMeta, bool) {
	s.rooms.mu.Lock()
	defer s.rooms.mu.Unlock()
	m := s.rooms.meta[sessionID]
	if m == nil {
		return roomMeta{}, false
	}
	out := *m
	out.properties = proto.Clone(m.properties).(*commonpb.MapValue)
	return out, true
}

// ErrRoomFull answers a join into a room with no space left.
//
// TODO(capture-needed): the status a real server answers with. Unconfirmed,
// so the code is the plain gRPC meaning rather than a guessed Nintendo one.
// Change: this value.
var ErrRoomFull = status.Error(codes.ResourceExhausted, "game session is full")

// joinRoom puts the caller into a room they were not matched into.
//
// This is not a weaker identity check. Who joins is still the authenticated
// caller, never a user the request names; what a room adds is who may join:
// anybody holding the room's name, which they get from its code or from a
// friend's presence, and the password if the host set one. The password is
// checked here because this is the only place the server can check it.
func (s *Server) joinRoom(ctx context.Context, sess matchmaking.Session, req *mmpb.JoinGameSessionRequest, userID string) (*mmpb.JoinGameSessionResponse, error) {
	meta, _ := s.roomMetaFor(sess.ID)
	if meta.password != "" && req.GetPassword() != meta.password {
		s.log.Info("npln: room join with the wrong password", "session", sess.ID, "user", userID)
		return nil, ErrPermissionDenied
	}
	joined, err := s.queue.Join(sess.ID, userID)
	switch {
	case errors.Is(err, matchmaking.ErrSessionFull):
		return nil, ErrRoomFull
	case err != nil:
		return nil, ErrNotFound
	}
	definition := s.concreteUserDefinition(userID, req.GetUserDefinitions())
	token, err := s.issueSessionTokenForDefinition(userID, joined.ID, definition)
	if err != nil {
		return nil, err
	}
	s.log.Info("npln: joined room", "session", joined.ID, "user", userID,
		"players", len(joined.Players), "capacity", joined.MaxPlayers)
	return &mmpb.JoinGameSessionResponse{
		GameSession: s.sessionProto(joined),
		MatchedUserSessions: []*mmpb.MatchedUserSession{{
			UserDefinition:     definition,
			UserSession:        s.userSessionName(joined.ID, userID),
			MatchmakingIdToken: token,
		}},
	}, nil
}

// roomCodeAlphabet leaves out O, 0, I and 1, so a code read off a friend's
// screen cannot be misread.
//
// TODO(capture-needed): the format of a real room code. This alphabet and
// length are NextendoNetwork's choice, made for a different title (their
// short-alias capture is Mario Party Jamboree's), and nothing shows what
// Splatoon 3 accepts when a player types one in. Change: this constant and
// newRoomCode.
const roomCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// newRoomCode draws six characters. 32 is a power of two, so the modulo
// below introduces no bias.
func newRoomCode() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = roomCodeAlphabet[int(b[i])%len(roomCodeAlphabet)]
	}
	return string(b)
}

// roomCodeTTL is the expiry a code is announced with. An hour is
// NextendoNetwork's choice, not a captured value; a zero timestamp would read
// as already expired. The code actually stops resolving when its room is
// closed or pruned, whichever comes first.
const roomCodeTTL = time.Hour

func (s *Server) roomCodeName(code string) string {
	return s.tenantPath() + "/gameSessionShortAliases/" + code
}

// CreateGameSessionShortAlias gives a room the code its host shows friends.
//
// The request names only the room; the server picks the code. It is stored
// in the room's _AliasSuffix property, which is where the game reads the Room
// ID it displays. Only a player already in the room may ask: a code is a key
// to the room, and minting one is the host's to do.
func (g *gameSessionService) CreateGameSessionShortAlias(
	ctx context.Context, req *mmpb.CreateGameSessionShortAliasRequest,
) (*mmpb.GameSessionShortAlias, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}
	id := lastSegment(req.GetGameSessionShortAlias().GetGameSession())
	sess, found := g.s.queue.Session(id)
	if !found || !sess.Room || !g.s.queue.IsPlayer(id, caller.UserID) {
		return nil, ErrNotFound
	}

	g.s.rooms.mu.Lock()
	meta := g.s.rooms.meta[id]
	if meta == nil {
		g.s.rooms.mu.Unlock()
		return nil, ErrNotFound
	}
	if meta.alias == "" {
		code := newRoomCode()
		for g.s.rooms.aliases[code] != "" {
			code = newRoomCode()
		}
		meta.alias = code
		g.s.rooms.aliases[code] = id
		meta.properties = roomProperties(meta.config, meta.properties, code)
	}
	code := meta.alias
	g.s.rooms.mu.Unlock()

	g.s.log.Info("npln: room code assigned", "session", id, "code", code)
	return &mmpb.GameSessionShortAlias{
		Name:        g.s.roomCodeName(code),
		GameSession: g.s.tenantPath() + "/gameSessions/" + id,
		ExpireTime:  timestamppb.New(g.s.now().Add(roomCodeTTL)),
	}, nil
}

// GetGameSessionShortAlias resolves a room code to its room: the first half
// of joining by code, JoinGameSession being the second.
func (g *gameSessionService) GetGameSessionShortAlias(
	ctx context.Context, req *mmpb.GetGameSessionShortAliasRequest,
) (*mmpb.GameSessionShortAlias, error) {
	if _, ok := CallerFrom(ctx); !ok {
		return nil, ErrUnauthenticated
	}
	code := strings.ToUpper(lastSegment(req.GetName()))
	g.s.rooms.mu.Lock()
	id := g.s.rooms.aliases[code]
	g.s.rooms.mu.Unlock()
	if id == "" {
		return nil, ErrNotFound
	}
	if _, ok := g.s.queue.Session(id); !ok {
		g.s.rooms.forget(id)
		return nil, ErrNotFound
	}
	return &mmpb.GameSessionShortAlias{
		Name:        g.s.roomCodeName(code),
		GameSession: g.s.tenantPath() + "/gameSessions/" + id,
		ExpireTime:  timestamppb.New(g.s.now().Add(roomCodeTTL)),
	}, nil
}
