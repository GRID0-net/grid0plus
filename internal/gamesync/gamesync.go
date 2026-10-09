// Package gamesync is NPLN's document and watch service — the live lobby state
// Splatoon 3 shares between the consoles in a session.
//
// Firestore-shaped: named documents in collections, with field masks, field
// transforms, preconditions, transactions and a bidirectional watch stream.
// Splatoon 3 reaches it on its own port (7575) rather than through the tenant
// host, so it is a second listener rather than another service on the gateway —
// but it runs inside cmd/npln, because it verifies tokens the gateway signs and
// splitting it would mean inventing a protocol between the two.
//
// # The store is in memory, deliberately
//
// A gamesync document is live lobby state: who is ready, which weapon somebody
// picked, how far a countdown has got. It is worth nothing once the lobby ends,
// and it is worth nothing across a restart — the consoles that owned it have
// already given up. This is the same reasoning internal/matchmaking's queue is
// built on. Save data is a different service (toyohr's CloudSave) and would be a
// different decision.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers
// Definitions: github.com/kinnay/NPLN-Protocols, generated into proto/
package gamesync

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

// TokenVerifier resolves a matchmaking id token to the player it names.
//
// A function rather than an import: the tokens are signed by internal/npln, and
// this package is served from the same process but must not depend on the
// gateway's internals. Returning an error rather than a bool so the caller can
// log why.
type TokenVerifier func(idToken string) (userID string, err error)

// Options configures the service.
type Options struct {
	Log *slog.Logger

	// VerifyIDToken resolves the matchmaking id token a console presents to
	// IssueToken. Required: without it there is no way to tell who is calling,
	// and the answer to that is never "assume".
	VerifyIDToken TokenVerifier
	// ResolveSession supplies verified matchmaking context for the room docs
	// watched immediately after IssueToken. It must reject a token that does
	// not belong to an active session and its named user session.
	ResolveSession func(idToken string) (SessionDetails, error)
	// LiveParticipants reads the current room roster for an authenticated
	// member. Room occupancy changes after IssueToken, while its details do not.
	LiveParticipants func(sessionID, userID string) (int, bool)
	// IssueAccessToken is an optional diagnostic signer for the gamesync
	// access token. The normal opaque token remains the default until hardware
	// confirms whether the client parses this field as a JWT.
	IssueAccessToken func(userID, userSession string, expiresAt time.Time) (string, error)

	// SessionTTL is how long a gamesync session lasts before it must be
	// refreshed. Zero means the default.
	SessionTTL time.Duration

	Now func() time.Time
	// OnRefereeReport records a completed match outside the transient lobby
	// document store. A nil callback leaves ordinary gamesync unchanged.
	OnRefereeReport func(context.Context, RefereeReport) error
	// PrepareCoopVerdict persists grade changes before clients can read their
	// refreshed attributes, and adds the same before/after values to history.
	PrepareCoopVerdict func(context.Context, RefereeReport) error

	// LobbyLogFlag is a file whose presence turns lobby-control logging on.
	// A file rather than a setting so the operator can flip it during a busy
	// fest without restarting npln and dropping every lobby; off by default,
	// because every console writes these documents several times a match.
	LobbyLogFlag string
}

// RefereeReport is the authenticated match context plus the game's final
// report. Its user and session IDs come from the verified gamesync token.
type RefereeReport struct {
	BattleID, SessionID, Mode, UserID string
	// Coop is true for a Salmon Run report (see isCoopReport).
	Coop    bool
	Players []string
	Fields  *commonpb.MapValue
	// Verdict is the referee result as published after this report: the
	// referee request's fields (stage, rule, mode, shift...) next to the
	// team result.
	Verdict *commonpb.MapValue
	// SessionUsers maps each live user-session id of this game session to
	// its user: a coop personal_result is keyed by user session.
	SessionUsers map[string]string
	// Profiles maps each player's NPLN user id to the player_info their
	// console wrote for this battle (see battleProfiles).
	Profiles map[string]*commonpb.Value
}

// defaultSessionTTL is how long a gamesync session lasts.
//
// An hour, which comfortably outlives a match. Long enough that a console does
// not refresh mid-battle; short enough that a session belonging to a console
// that vanished is not held indefinitely.
const defaultSessionTTL = time.Hour

// Server is the gamesync service.
type Server struct {
	syncpb.UnimplementedGamesyncServer

	log                *slog.Logger
	verify             TokenVerifier
	resolveSession     func(string) (SessionDetails, error)
	liveParticipants   func(sessionID, userID string) (int, bool)
	issueAccessToken   func(userID, userSession string, expiresAt time.Time) (string, error)
	onRefereeReport    func(context.Context, RefereeReport) error
	prepareCoopVerdict func(context.Context, RefereeReport) error
	sessionTTL         time.Duration
	refreshEvery       time.Duration
	now                func() time.Time

	docs  *storeSet
	watch *watchers
	// again holds public sessions' "Again!" grace windows.
	again   againQueue
	mailbox peerMailbox
	// A report from any player may update the same battle verdict. Serialize
	// read/merge/write so simultaneous reports cannot discard one another.
	refereeMu sync.Mutex
	// Serialize durable snapshots and callbacks so an older partial result
	// cannot overwrite a newer accumulated history after a slow DB write.
	reportMu sync.Mutex

	// cleanups holds deferments and open watch streams per user-session
	// name, not per token: see userSessionCleanup.
	cleanupMu sync.Mutex
	cleanups  map[string]*userSessionCleanup

	mu       sync.Mutex
	sessions map[string]*session
	// txns are the open transactions, by their opaque token.
	txns map[string]*transaction

	grpc *grpc.Server

	lobbyLogFlag string
	lobbyLog     atomic.Bool
}

// session is one console's authenticated gamesync session.
type session struct {
	mu           sync.Mutex
	userID       string
	userSession  string
	accessToken  string
	refreshToken string
	createdAt    time.Time
	expiresAt    time.Time
	details      *SessionDetails
}

// New builds the service.
func New(opts Options) (*Server, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.VerifyIDToken == nil {
		// Fail-closed at construction. A service that cannot identify a caller
		// and starts anyway is a service that will eventually answer one
		// player's lobby with another's.
		return nil, errors.New("gamesync: no token verifier")
	}
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = defaultSessionTTL
	}

	s := &Server{
		log:                opts.Log,
		verify:             opts.VerifyIDToken,
		resolveSession:     opts.ResolveSession,
		liveParticipants:   opts.LiveParticipants,
		issueAccessToken:   opts.IssueAccessToken,
		onRefereeReport:    opts.OnRefereeReport,
		prepareCoopVerdict: opts.PrepareCoopVerdict,
		sessionTTL:         opts.SessionTTL,
		lobbyLogFlag:       opts.LobbyLogFlag,
		refreshEvery:       3 * time.Second,
		now:                opts.Now,
		docs:               newStoreSet(opts.Now),
		watch:              newWatchers(),
		sessions:           map[string]*session{},
		cleanups:           map[string]*userSessionCleanup{},
		txns:               map[string]*transaction{},
	}
	s.watch.startup = s.startupVerdict
	s.grpc = grpc.NewServer(
		grpc.ChainUnaryInterceptor(s.unaryInterceptor),
		grpc.ChainStreamInterceptor(s.streamInterceptor),
	)
	syncpb.RegisterGamesyncServer(s.grpc, s)
	return s, nil
}

// Serve accepts connections until the listener is closed.
//
// Cleartext HTTP/2, like the gateway: TLS is terminated at the nginx edge, which
// already holds the certificate for every Nintendo hostname.
func (s *Server) Serve(l net.Listener) error { return s.grpc.Serve(l) }

// Stop stops the server, waiting for in-flight RPCs.
func (s *Server) Stop() { s.grpc.GracefulStop() }

// GRPCServer exposes the underlying server, for tests.
func (s *Server) GRPCServer() *grpc.Server { return s.grpc }

// mdAuthorization carries "Bearer <gamesync access token>".
//
// Lower-case because gRPC lower-cases metadata keys on the wire: looking one up
// by the capitalisation a capture shows finds nothing.
const mdAuthorization = "authorization"

// bearerFrom pulls the access token out of a call's metadata.
func bearerFrom(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get(mdAuthorization)
	if len(values) == 0 {
		return ""
	}
	v := strings.TrimSpace(values[0])
	// Case-insensitive on the scheme: RFC 7235 says it is, and a client sending
	// "bearer" is not malformed.
	if len(v) >= 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

// ---- authentication -------------------------------------------------------

// openMethods are the RPCs callable without a gamesync access token, because
// they are how one is obtained.
//
// An allowlist, for the reason internal/npln gives: a new RPC is authenticated
// until somebody says otherwise, rather than public until somebody remembers.
var openMethods = map[string]bool{
	"/nn.npln.gamesync.v1.Gamesync/IssueToken":   true,
	"/nn.npln.gamesync.v1.Gamesync/RefreshToken": true,
}

type callerKey struct{}

// callerFrom returns the authenticated player.
func callerFrom(ctx context.Context) (*session, bool) {
	c, ok := ctx.Value(callerKey{}).(*session)
	return c, ok
}

func (s *Server) unaryInterceptor(
	ctx context.Context, req any,
	info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
) (any, error) {
	_ = grpc.SetHeader(ctx, metadata.Pairs("npln-grpc-type", "Unary"))
	ctx, err := s.enter(ctx, info.FullMethod)
	if err != nil {
		s.log.Info("gamesync: call", "method", info.FullMethod, "code", status.Code(err))
		return nil, err
	}
	resp, err := handler(ctx, req)
	s.log.Info("gamesync: call", "method", info.FullMethod, "code", status.Code(err))
	return resp, err
}

func (s *Server) streamInterceptor(
	srv any, ss grpc.ServerStream,
	info *grpc.StreamServerInfo, handler grpc.StreamHandler,
) error {
	_ = ss.SetHeader(metadata.Pairs("npln-grpc-type", grpcKind(info)))
	ctx, err := s.enter(ss.Context(), info.FullMethod)
	if err != nil {
		s.log.Info("gamesync: stream refused", "method", info.FullMethod, "code", status.Code(err))
		return err
	}
	s.log.Info("gamesync: stream open", "method", info.FullMethod)
	err = handler(srv, wrappedStream{ServerStream: ss, ctx: ctx})
	s.log.Info("gamesync: stream closed", "method", info.FullMethod, "code", status.Code(err))
	return err
}

// grpcKind is the value Nintendo's NPLN gateway puts in npln-grpc-type: the
// kind of call. A capture of 704 responses from the real tenant host carries it
// on every one -- Unary, ServerStreaming or BidirectionalStreaming -- per
// NextendoNetwork/splatoon-3's measurement (facts only, not its code).
func grpcKind(info *grpc.StreamServerInfo) string {
	switch {
	case info.IsClientStream && info.IsServerStream:
		return "BidirectionalStreaming"
	case info.IsServerStream:
		return "ServerStreaming"
	case info.IsClientStream:
		return "ClientStreaming"
	}
	return "Unary"
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w wrappedStream) Context() context.Context { return w.ctx }

func (s *Server) enter(ctx context.Context, method string) (context.Context, error) {
	if openMethods[method] {
		return ctx, nil
	}
	token := bearerFrom(ctx)
	if token == "" {
		s.log.Info("gamesync: call arrived without a session token", "method", method)
		return nil, errUnauthenticated
	}

	s.mu.Lock()
	sess, ok := s.sessions[token]
	s.mu.Unlock()

	if !ok {
		s.log.Info("gamesync: call presented an unknown session token", "method", method)
		return nil, errUnauthenticated
	}
	if !s.now().Before(sess.expiresAt) {
		// Expired. Removed rather than left to accumulate, and refused rather
		// than renewed: renewing on use would make the TTL meaningless.
		s.mu.Lock()
		delete(s.sessions, token)
		s.mu.Unlock()
		return nil, errUnauthenticated
	}
	return context.WithValue(ctx, callerKey{}, sess), nil
}

// IssueToken exchanges a matchmaking id token for a gamesync session.
//
// The id token is verified, not read. It is signed by the gateway with the same
// key gamesync's verifier holds, so a console cannot name itself: the token says
// who it is and the signature is what makes that binding.
func (s *Server) IssueToken(
	ctx context.Context, req *syncpb.IssueTokenRequest,
) (*syncpb.IssueTokenResponse, error) {
	userID, err := s.verify(req.GetMatchmakingIdToken())
	if err != nil {
		s.log.Debug("gamesync: rejected a matchmaking id token", "error", err)
		return nil, errUnauthenticated
	}
	s.log.Info("gamesync: client user session name", "name", req.GetUserSession())
	var details *SessionDetails
	if s.resolveSession != nil {
		resolved, err := s.resolveSession(req.GetMatchmakingIdToken())
		if err != nil || resolved.UserID != userID || resolved.UserSessionID != sessionID(req.GetUserSession()) {
			return nil, errUnauthenticated
		}
		details = &resolved
	}

	sess, err := s.newSession(userID, req.GetUserSession())
	if err != nil {
		s.log.Error("gamesync: cannot issue session", "user", userID, "error", err)
		return nil, status.Error(codes.Internal, "cannot issue gamesync session")
	}
	sess.details = details
	s.log.Info("gamesync: issued a session", "user", userID)
	return &syncpb.IssueTokenResponse{Token: s.tokenProto(sess)}, nil
}

// RefreshToken renews a session.
//
// Matched on the refresh token rather than on the access token, because the
// point of refreshing is that the access token has expired. The old session is
// replaced rather than extended, so a refresh token is single-use — a leaked one
// stops working the moment the legitimate console uses it.
func (s *Server) RefreshToken(
	ctx context.Context, req *syncpb.RefreshTokenRequest,
) (*syncpb.RefreshTokenResponse, error) {
	presented := req.GetRefreshToken()
	if presented == "" {
		return nil, errUnauthenticated
	}

	s.mu.Lock()
	var found *session
	for token, sess := range s.sessions {
		if sess.refreshToken == presented {
			found = sess
			delete(s.sessions, token)
			break
		}
	}
	s.mu.Unlock()

	if found == nil {
		return nil, errUnauthenticated
	}

	sess, err := s.newSession(found.userID, found.userSession)
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot refresh gamesync session")
	}
	sess.details = found.details
	return &syncpb.RefreshTokenResponse{Token: s.tokenProto(sess)}, nil
}

func (s *Server) newSession(userID, userSession string) (*session, error) {
	sess := &session{
		userID:       userID,
		userSession:  userSession,
		accessToken:  randomToken(),
		refreshToken: randomToken(),
		createdAt:    s.now(),
		expiresAt:    s.now().Add(s.sessionTTL),
	}
	if s.issueAccessToken != nil {
		var err error
		sess.accessToken, err = s.issueAccessToken(userID, userSession, sess.expiresAt)
		if err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.sessions[sess.accessToken] = sess
	s.mu.Unlock()
	return sess, nil
}

func (s *Server) tokenProto(sess *session) *syncpb.Token {
	return &syncpb.Token{
		UserSession:  sess.userSession,
		AccessToken:  sess.accessToken,
		RefreshToken: sess.refreshToken,
		Ttl:          durationpb.New(s.sessionTTL),
	}
}

// ---- reads ---------------------------------------------------------------

// GetDocument returns one document.
func (s *Server) GetDocument(
	ctx context.Context, req *syncpb.GetDocumentRequest,
) (*syncpb.Document, error) {
	caller, ok := callerFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	doc, ok := s.watchDocument(caller, req.GetName())
	if !ok {
		s.log.Info("gamesync: document missing", "name", req.GetName())
		return nil, status.Error(codes.NotFound, "no such document")
	}
	s.log.Info("gamesync: document read", "name", req.GetName())
	return applyReadMask(doc, req.GetReadMask().GetPaths()), nil
}

// ReadDocuments returns several documents at once.
//
// A document that does not exist comes back as `missing` rather than being left
// out. The client asked about a specific list, and a short answer means it
// cannot tell "absent" from "the server did not look".
func (s *Server) ReadDocuments(
	ctx context.Context, req *syncpb.ReadDocumentsRequest,
) (*syncpb.ReadDocumentsResponse, error) {
	caller, ok := callerFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}

	mask := req.GetReadMask().GetPaths()
	results := make([]*syncpb.ReadResult, 0, len(req.GetDocuments()))
	for _, name := range req.GetDocuments() {
		if doc, ok := s.watchDocument(caller, name); ok {
			results = append(results, &syncpb.ReadResult{
				Result: &syncpb.ReadResult_Found{Found: applyReadMask(doc, mask)},
			})
			continue
		}
		results = append(results, &syncpb.ReadResult{
			Result: &syncpb.ReadResult_Missing{Missing: name},
		})
	}

	return &syncpb.ReadDocumentsResponse{
		Results:  results,
		ReadTime: timestamppb.New(s.now()),
	}, nil
}

// ListDocuments returns the documents directly under a collection.
func (s *Server) ListDocuments(
	ctx context.Context, req *syncpb.ListDocumentsRequest,
) (*syncpb.ListDocumentsResponse, error) {
	caller, ok := callerFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}

	docs := s.collectionSnapshot(caller, req.GetParent())
	mask := req.GetReadMask().GetPaths()
	out := make([]*syncpb.Document, 0, len(docs))
	for _, doc := range docs {
		out = append(out, applyReadMask(doc, mask))
	}
	// No paging. The whole store is one lobby's live state -- tens of documents,
	// not thousands -- so a page token would be a token for a page that is
	// always the only one. If that stops being true, the absence is visible
	// here rather than hidden behind an empty next_page_token.
	return &syncpb.ListDocumentsResponse{Documents: out}, nil
}

// QueryCollectionIds returns the collection names directly under a document.
func (s *Server) QueryCollectionIds(
	ctx context.Context, req *syncpb.QueryCollectionIdsRequest,
) (*syncpb.QueryCollectionIdsResponse, error) {
	caller, ok := callerFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	return &syncpb.QueryCollectionIdsResponse{
		CollectionIds: s.docsFor(caller).collections(req.GetDocument()),
	}, nil
}

// ---- writes --------------------------------------------------------------

// WriteDocuments applies a batch of writes.
//
// **Atomic.** Every precondition is checked before anything is written, and a
// single failure applies nothing. A half-applied batch is worse than a rejected
// one here: the client's next read sees a state neither it nor the server ever
// intended, and it has no way to tell which half landed.
func (s *Server) WriteDocuments(
	ctx context.Context, req *syncpb.WriteDocumentsRequest,
) (*syncpb.WriteDocumentsResponse, error) {
	caller, ok := callerFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	prepared, err := ownedReportWrites(caller, req.GetWriteOperations())
	if err != nil {
		return nil, err
	}
	req = proto.Clone(req).(*syncpb.WriteDocumentsRequest)
	req.WriteOperations = prepared
	s.logLobbyControlWrites(caller, prepared)
	if response, duplicate := s.duplicateRefereeKickoff(caller, req); duplicate {
		return response, nil
	}
	if len(req.GetDefermentOperations()) > 0 {
		if err := validateDeferments(req.GetDefermentOperations()); err != nil {
			return nil, err
		}
		for _, operation := range req.GetDefermentOperations() {
			if update := operation.GetUpdateDeferment(); update != nil {
				prepared, err := ownedReportWrites(caller, update.GetDeferment().GetWriteOperations())
				if err != nil {
					return nil, err
				}
				update.Deferment.WriteOperations = prepared
			}
		}
	}

	s.seedRoomDocuments(caller, req.GetWriteOperations())
	results, commit, err := s.docsFor(caller).apply(req.GetWriteOperations())
	if err == nil {
		rankSessionCounter(caller, req.GetWriteOperations(), results)
	}
	if err != nil {
		s.log.Warn("gamesync: write rejected", "writes", len(req.GetWriteOperations()),
			"error", err)
		return nil, err
	}
	s.afterWrites(ctx, caller, req.GetWriteOperations(), commit.changes)
	if len(req.GetDefermentOperations()) > 0 {
		caller, _ := callerFrom(ctx)
		s.stageDeferments(caller, req.GetDefermentOperations())
	}

	return &syncpb.WriteDocumentsResponse{
		WriteResults: results,
		CommitTime:   timestamppb.New(commit.at),
	}, nil
}

// LazyWriteDocuments applies writes that the client does not need confirmed.
//
// Applied immediately rather than lazily. "Lazy" is the client saying it will
// not wait for per-write results — the response carries only a commit time —
// not that the server may lose them. Deferring for real would mean a durability
// story an in-memory store cannot honour.
func (s *Server) LazyWriteDocuments(
	ctx context.Context, req *syncpb.LazyWriteDocumentsRequest,
) (*syncpb.LazyWriteDocumentsResponse, error) {
	caller, ok := callerFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}

	ops := make([]*syncpb.WriteOperation, 0, len(req.GetWriteOperations()))
	for _, lazy := range req.GetWriteOperations() {
		switch op := lazy.GetOperationType().(type) {
		case *syncpb.LazyWriteOperation_MergeDocument:
			ops = append(ops, &syncpb.WriteOperation{
				OperationType: &syncpb.WriteOperation_MergeDocument{MergeDocument: op.MergeDocument},
			})
		case *syncpb.LazyWriteOperation_TransformDocument:
			ops = append(ops, &syncpb.WriteOperation{
				OperationType: &syncpb.WriteOperation_TransformDocument{TransformDocument: op.TransformDocument},
			})
		default:
			return nil, status.Error(codes.InvalidArgument, "unsupported lazy write")
		}
	}
	s.logLobbyControlWrites(caller, ops)

	prepared, err := ownedReportWrites(caller, ops)
	if err != nil {
		return nil, err
	}
	ops = prepared
	s.seedRoomDocuments(caller, ops)
	_, commit, err := s.docsFor(caller).apply(ops)
	if err != nil {
		return nil, err
	}
	s.afterWrites(ctx, caller, ops, commit.changes)

	return &syncpb.LazyWriteDocumentsResponse{CommitTime: timestamppb.New(commit.at)}, nil
}

// ---- transactions --------------------------------------------------------

// transaction is a set of writes waiting to be committed together.
type transaction struct {
	userID    string
	scope     string
	startedAt time.Time
}

// BeginTransaction opens a transaction and returns its opaque token.
func (s *Server) BeginTransaction(
	ctx context.Context, _ *syncpb.BeginTransactionRequest,
) (*syncpb.BeginTransactionResponse, error) {
	caller, ok := callerFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}

	token := randomToken()
	s.mu.Lock()
	s.txns[token] = &transaction{userID: caller.userID, scope: scopeOf(caller), startedAt: s.now()}
	s.mu.Unlock()

	return &syncpb.BeginTransactionResponse{Transaction: []byte(token)}, nil
}

// CommitTransaction applies a transaction's writes.
//
// The writes arrive here rather than being accumulated as the transaction runs,
// which is how the protocol is shaped — so a transaction is a token proving the
// client meant one batch, and the atomicity comes from apply. Committing an
// unknown or already-committed transaction fails rather than applying the
// writes anyway: a client retrying a commit it believes failed must not double
// a field it incremented.
func (s *Server) CommitTransaction(
	ctx context.Context, req *syncpb.CommitTransactionRequest,
) (*syncpb.CommitTransactionResponse, error) {
	caller, ok := callerFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if len(req.GetDefermentOperations()) > 0 {
		return nil, status.Error(codes.Unimplemented, "")
	}

	prepared, err := ownedReportWrites(caller, req.GetWriteOperations())
	if err != nil {
		return nil, err
	}
	req = proto.Clone(req).(*syncpb.CommitTransactionRequest)
	req.WriteOperations = prepared
	s.logLobbyControlWrites(caller, prepared)
	if err := s.claimTransaction(string(req.GetTransaction()), caller); err != nil {
		return nil, err
	}

	s.seedRoomDocuments(caller, req.GetWriteOperations())
	results, commit, err := s.docsFor(caller).apply(req.GetWriteOperations())
	if err == nil {
		rankSessionCounter(caller, req.GetWriteOperations(), results)
	}
	if err != nil {
		return nil, err
	}
	s.afterWrites(ctx, caller, req.GetWriteOperations(), commit.changes)

	return &syncpb.CommitTransactionResponse{
		WriteResults: results,
		CommitTime:   timestamppb.New(commit.at),
	}, nil
}

// RollbackTransaction discards a transaction.
func (s *Server) RollbackTransaction(
	ctx context.Context, req *syncpb.RollbackTransactionRequest,
) (*emptypb.Empty, error) {
	caller, ok := callerFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	// A rollback of something that is already gone is not an error: a client
	// that lost its connection mid-transaction retries the rollback, and
	// failing it leaves the client believing state is still pending.
	_ = s.claimTransaction(string(req.GetTransaction()), caller)
	return &emptypb.Empty{}, nil
}

// claimTransaction removes a transaction, checking that it belongs to the
// caller.
func (s *Server) claimTransaction(token string, caller *session) error {
	if token == "" {
		return status.Error(codes.InvalidArgument, "no transaction")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	txn, ok := s.txns[token]
	if !ok {
		return status.Error(codes.FailedPrecondition, "unknown or already-finished transaction")
	}
	if txn.userID != caller.userID || txn.scope != scopeOf(caller) {
		// Somebody else's transaction. NotFound rather than PermissionDenied,
		// so a caller does not learn that a token they guessed is real.
		return status.Error(codes.FailedPrecondition, "unknown or already-finished transaction")
	}
	delete(s.txns, token)
	return nil
}

// ---- the watch stream ----------------------------------------------------

// KeepUserSession is the bidirectional stream a console keeps open.
//
// Three things arrive on it: an echo, which the client uses to prove the stream
// is alive; a target to start watching; and a target to stop watching. Document
// changes flow back.
//
// **The stream is never closed from this side while the console holds it.** A
// stream that returns is read as the connection failing rather than as "nothing
// to report" — the same rule SubscribeMaintenanceSchedules and the frontline
// connection follow.
func (s *Server) KeepUserSession(
	stream grpc.BidiStreamingServer[syncpb.KeepUserSessionRequest, syncpb.KeepUserSessionResponse],
) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	caller, ok := callerFrom(ctx)
	if !ok {
		return errUnauthenticated
	}

	sessionID := ""
	if caller.details != nil {
		sessionID = caller.details.SessionID
	}
	sub := s.watch.subscribe(caller.userID, caller.userSession, sessionID)
	refresh := time.NewTicker(s.refreshEvery)
	lastRefresh := make(map[string]*commonpb.MapValue)
	defer refresh.Stop()
	refreshLogged := false
	s.beginSessionStream(caller)
	defer func() {
		s.watch.unsubscribe(sub)
		s.endSessionStream(caller)
	}()

	// Writes go out from one goroutine only. gRPC allows one concurrent Send
	// per stream, and the reader loop below sends echoes while changes arrive
	// from the store -- two senders would be a data race the transport does not
	// protect against.
	out := make(chan *syncpb.KeepUserSessionResponse, 16)
	done := make(chan struct{})

	go func() {
		for {
			req, err := stream.Recv()
			if err != nil {
				// Either io.EOF -- the console closing its half -- or a
				// cancelled context. Neither is a failure; both mean stop.
				close(done)
				return
			}
			s.handleWatchRequest(ctx, caller, sub, req, out)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sub.overflow:
			return status.Error(codes.ResourceExhausted, "watch fell behind; reconnect for a fresh snapshot")
		case <-done:
			// The console stopped sending. It may still be reading, but there
			// is nothing left to tell it about: its targets went with the
			// stream.
			return nil
		case <-refresh.C:
			messages := s.refreshSessionDocuments(caller, sub, lastRefresh)
			if len(messages) > 0 && !refreshLogged {
				s.log.Info("gamesync: live session refresh started", "user", caller.userID,
					"session", sessionID, "messages", len(messages))
				refreshLogged = true
			}
			for _, msg := range messages {
				if err := stream.Send(msg); err != nil {
					return err
				}
			}
		case msg := <-out:
			if err := stream.Send(msg); err != nil {
				return err
			}
		case change := <-sub.changes:
			if err := stream.Send(&syncpb.KeepUserSessionResponse{
				ResponseType: &syncpb.KeepUserSessionResponse_DocumentChange{
					DocumentChange: change,
				},
			}); err != nil {
				return err
			}
			// Splatoon buffers live document changes until a target boundary.
			// The initial snapshot has LISTED, but later writes need their own
			// boundary or the room never observes its session documents.
			if err := stream.Send(&syncpb.KeepUserSessionResponse{
				ResponseType: &syncpb.KeepUserSessionResponse_TargetChange{
					TargetChange: &syncpb.TargetChange{
						TargetId: change.GetTargetId(), TargetChangeType: syncpb.TargetChange_UPDATED,
					},
				},
			}); err != nil {
				return err
			}
		}
	}
}

func (s *Server) handleWatchRequest(
	ctx context.Context, caller *session, sub *subscriber, req *syncpb.KeepUserSessionRequest,
	out chan<- *syncpb.KeepUserSessionResponse,
) {
	switch r := req.GetRequestType().(type) {
	case *syncpb.KeepUserSessionRequest_Echo:
		// Echoed back verbatim. It is the client's own liveness check, so
		// changing the payload would defeat the only thing it is for.
		send(ctx, out, &syncpb.KeepUserSessionResponse{
			ResponseType: &syncpb.KeepUserSessionResponse_Echo{Echo: r.Echo},
		})

	case *syncpb.KeepUserSessionRequest_UpdateTarget:
		target := r.UpdateTarget.GetTarget()
		if target == nil || target.GetName() == "" {
			return
		}
		s.log.Info("gamesync: watch target", "name", target.GetName(),
			"documents", target.GetDocuments().GetDocuments(), "collection", target.GetCollection().GetCollection())
		s.watch.addTarget(sub, target)
		id := wireTargetID(target.GetName())
		targetChange := func(kind syncpb.TargetChange_TargetChangeType) {
			send(ctx, out, &syncpb.KeepUserSessionResponse{
				ResponseType: &syncpb.KeepUserSessionResponse_TargetChange{
					TargetChange: &syncpb.TargetChange{TargetId: id, TargetChangeType: kind},
				},
			})
		}
		// LISTED closes the initial snapshot. Sending it before the documents
		// makes the client conclude its own UserSession is absent and wait until
		// the room-creation operation times out.
		targetChange(syncpb.TargetChange_UPDATED)
		var docs []*syncpb.Document
		if list := target.GetDocuments(); list != nil {
			docs = s.watchedDocuments(caller, list.GetDocuments())
		} else if coll := target.GetCollection(); coll != nil {
			docs = s.collectionSnapshot(caller, coll.GetCollection())
		}
		for _, doc := range docs {
			send(ctx, out, &syncpb.KeepUserSessionResponse{
				ResponseType: &syncpb.KeepUserSessionResponse_DocumentChange{
					DocumentChange: &syncpb.DocumentChange{
						TargetId: id, DocumentChangeType: syncpb.DocumentChange_EXIST, Document: doc,
					},
				},
			})
		}
		s.log.Info("gamesync: initial watch snapshot", "target", target.GetName(), "documents", len(docs))
		targetChange(syncpb.TargetChange_LISTED)

	case *syncpb.KeepUserSessionRequest_DeleteTarget:
		name := r.DeleteTarget.GetName()
		s.watch.removeTarget(sub, name)
		send(ctx, out, &syncpb.KeepUserSessionResponse{
			ResponseType: &syncpb.KeepUserSessionResponse_TargetChange{
				TargetChange: &syncpb.TargetChange{
					TargetId:         wireTargetID(name),
					TargetChangeType: syncpb.TargetChange_DELETED,
				},
			},
		})
	}
}

// Backpressure preserves the snapshot boundary and every document preceding it.
// Cancellation releases the receiver even if the stream's writer has failed.
func send(ctx context.Context, out chan<- *syncpb.KeepUserSessionResponse, msg *syncpb.KeepUserSessionResponse) {
	select {
	case out <- msg:
	case <-ctx.Done():
	}
}

// CreateRound echoes back the round the client sent.
//
// Round has five fields and four are Unk1..Unk4 in the decompiled
// definitions: nobody knows what they mean, so this server sets none of them.
// Echoing sets nothing either -- every value in the answer is the client's
// own. It used to answer Unimplemented, which Splatoon 3 treats as a hard
// failure of whatever called it (2321-4224 is the same thing for another
// RPC). The game does call it: a 2026-09-20 decompile of v11.3.0 finds its
// path referenced from real call sites. NextendoNetwork/splatoon-3 answers
// the same echo (facts only, none of its code used), and its server plays
// matches through to their results with it.
//
// TODO(capture-needed): whether a real server fills in any field the
// request left empty (a round id, most likely). Change: this method.
func (s *Server) CreateRound(
	ctx context.Context, req *syncpb.CreateRoundRequest,
) (*syncpb.Round, error) {
	if _, ok := callerFrom(ctx); !ok {
		return nil, errUnauthenticated
	}
	if r := req.GetRound(); r != nil {
		return r, nil
	}
	return &syncpb.Round{}, nil
}

// ---- helpers -------------------------------------------------------------

var errUnauthenticated = status.Error(codes.Unauthenticated, "unauthenticated")

// randomToken returns an opaque, unguessable token.
//
// Random rather than a signed JWT: these are session handles held in this
// process's own map, so there is nothing for a signature to buy. Anything that
// has to survive a restart is signed instead — see internal/jwt.
func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any platform this runs on, and a
		// predictable session token would be worse than a crash.
		panic(fmt.Sprintf("gamesync: no randomness: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// applyReadMask returns a copy of doc carrying only the named fields.
//
// A copy, always: handing out the stored message would let a caller mutate the
// store through the value it was given, and the whole document tree is shared
// between every console in the lobby.
func applyReadMask(doc *syncpb.Document, paths []string) *syncpb.Document {
	out := proto.Clone(doc).(*syncpb.Document)
	if len(paths) == 0 || (len(paths) == 1 && paths[0] == "*") {
		return out
	}

	keep := make(map[string]bool, len(paths))
	for _, p := range paths {
		// Only the first segment. A nested mask would need field-level pruning
		// inside a MapValue, and pruning a path this code does not understand
		// would silently drop data the client asked for. Keeping the whole
		// top-level field errs towards sending too much, which is the harmless
		// direction.
		if p == "*" {
			return out
		}
		keep[maskTopLevel(p)] = true
	}
	if out.Fields == nil {
		return out
	}
	for name := range out.Fields.GetFields() {
		if !keep[name] {
			delete(out.Fields.Fields, name)
		}
	}
	return out
}

// sortedNames returns map keys in a stable order.
//
// Deterministic because a list response that reorders between two identical
// calls makes a client's diff show changes that did not happen.
func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// emptyMap builds an empty field map, so a document always has one.
//
// A nil Fields and an empty one are the same on the wire, but not in this code:
// every transform and merge writes into it, and a nil map panics on write.
func emptyMap() *commonpb.MapValue {
	return &commonpb.MapValue{Fields: map[string]*commonpb.Value{}}
}

// logLobbyControlWrites logs what a console writes to the lobby-control
// documents (docs/__gs/*, docs/SessionInfo/*): backfill, lock and start flags
// are only visible there, and a lobby that never starts is diagnosed from them.
func (s *Server) logLobbyControlWrites(caller *session, ops []*syncpb.WriteOperation) {
	if !s.lobbyLog.Load() {
		return
	}
	for _, op := range ops {
		doc := op.GetUpdateDocument().GetDocument()
		if doc == nil {
			doc = op.GetMergeDocument().GetDocument()
		}
		name := doc.GetName()
		if !strings.HasPrefix(name, "docs/__gs/") && !strings.HasPrefix(name, "docs/SessionInfo") {
			continue
		}
		mode := ""
		if caller.details != nil {
			mode = caller.details.Mode
		}
		body, _ := protojson.Marshal(doc.GetFields())
		s.log.Info("gamesync: lobby control write", "user", caller.userID, "mode", mode,
			"doc", name, "fields", string(body))
	}
}

// WatchLobbyLogFlag polls LobbyLogFlag until ctx ends, so creating or removing
// the file turns lobby-control logging on or off within a few seconds.
func (s *Server) WatchLobbyLogFlag(ctx context.Context, every time.Duration) {
	if s.lobbyLogFlag == "" {
		return
	}
	check := func() {
		_, err := os.Stat(s.lobbyLogFlag)
		on := err == nil
		if s.lobbyLog.Swap(on) != on {
			s.log.Info("gamesync: lobby control logging", "enabled", on, "flag", s.lobbyLogFlag)
		}
	}
	check()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}
