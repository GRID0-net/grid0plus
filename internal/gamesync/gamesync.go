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
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	syncpb "github.com/n-popescu/switchnet/proto/gamesync/v1"
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
	// IssueAccessToken is an optional diagnostic signer for the gamesync
	// access token. The normal opaque token remains the default until hardware
	// confirms whether the client parses this field as a JWT.
	IssueAccessToken func(userID, userSession string, expiresAt time.Time) (string, error)

	// SessionTTL is how long a gamesync session lasts before it must be
	// refreshed. Zero means the default.
	SessionTTL time.Duration

	Now func() time.Time
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

	log              *slog.Logger
	verify           TokenVerifier
	issueAccessToken func(userID, userSession string, expiresAt time.Time) (string, error)
	sessionTTL       time.Duration
	now              func() time.Time

	docs  *store
	watch *watchers

	mu       sync.Mutex
	sessions map[string]*session
	// txns are the open transactions, by their opaque token.
	txns map[string]*transaction

	grpc *grpc.Server
}

// session is one console's authenticated gamesync session.
type session struct {
	userID       string
	userSession  string
	accessToken  string
	refreshToken string
	expiresAt    time.Time
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
		log:              opts.Log,
		verify:           opts.VerifyIDToken,
		issueAccessToken: opts.IssueAccessToken,
		sessionTTL:       opts.SessionTTL,
		now:              opts.Now,
		docs:             newStore(opts.Now),
		watch:            newWatchers(),
		sessions:         map[string]*session{},
		txns:             map[string]*transaction{},
	}
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
		return nil, err
	}
	return handler(ctx, req)
}

func (s *Server) streamInterceptor(
	srv any, ss grpc.ServerStream,
	info *grpc.StreamServerInfo, handler grpc.StreamHandler,
) error {
	_ = ss.SetHeader(metadata.Pairs("npln-grpc-type", grpcKind(info)))
	ctx, err := s.enter(ss.Context(), info.FullMethod)
	if err != nil {
		return err
	}
	return handler(srv, wrappedStream{ServerStream: ss, ctx: ctx})
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

	sess, err := s.newSession(userID, req.GetUserSession())
	if err != nil {
		s.log.Error("gamesync: cannot issue session", "user", userID, "error", err)
		return nil, status.Error(codes.Internal, "cannot issue gamesync session")
	}
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
	return &syncpb.RefreshTokenResponse{Token: s.tokenProto(sess)}, nil
}

func (s *Server) newSession(userID, userSession string) (*session, error) {
	sess := &session{
		userID:       userID,
		userSession:  userSession,
		accessToken:  randomToken(),
		refreshToken: randomToken(),
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
	if _, ok := callerFrom(ctx); !ok {
		return nil, errUnauthenticated
	}
	doc, ok := s.docs.get(req.GetName())
	if !ok {
		return nil, status.Error(codes.NotFound, "no such document")
	}
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
	if _, ok := callerFrom(ctx); !ok {
		return nil, errUnauthenticated
	}

	mask := req.GetReadMask().GetPaths()
	results := make([]*syncpb.ReadResult, 0, len(req.GetDocuments()))
	for _, name := range req.GetDocuments() {
		if doc, ok := s.docs.get(name); ok {
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
	if _, ok := callerFrom(ctx); !ok {
		return nil, errUnauthenticated
	}

	docs := s.docs.list(req.GetParent())
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
	if _, ok := callerFrom(ctx); !ok {
		return nil, errUnauthenticated
	}
	return &syncpb.QueryCollectionIdsResponse{
		CollectionIds: s.docs.collections(req.GetDocument()),
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
	if _, ok := callerFrom(ctx); !ok {
		return nil, errUnauthenticated
	}
	if len(req.GetDefermentOperations()) > 0 {
		// A deferment is a batch of writes to apply later, on a trigger nothing
		// here models. Refused rather than dropped: a client that queued a
		// write for later and was told "done" would wait for an effect that
		// never arrives.
		return nil, status.Error(codes.Unimplemented, "")
	}

	results, commit, err := s.docs.apply(req.GetWriteOperations())
	if err != nil {
		return nil, err
	}
	s.watch.publish(commit.changes)

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
	if _, ok := callerFrom(ctx); !ok {
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

	_, commit, err := s.docs.apply(ops)
	if err != nil {
		return nil, err
	}
	s.watch.publish(commit.changes)

	return &syncpb.LazyWriteDocumentsResponse{CommitTime: timestamppb.New(commit.at)}, nil
}

// ---- transactions --------------------------------------------------------

// transaction is a set of writes waiting to be committed together.
type transaction struct {
	userID    string
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
	s.txns[token] = &transaction{userID: caller.userID, startedAt: s.now()}
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

	if err := s.claimTransaction(string(req.GetTransaction()), caller.userID); err != nil {
		return nil, err
	}

	results, commit, err := s.docs.apply(req.GetWriteOperations())
	if err != nil {
		return nil, err
	}
	s.watch.publish(commit.changes)

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
	_ = s.claimTransaction(string(req.GetTransaction()), caller.userID)
	return &emptypb.Empty{}, nil
}

// claimTransaction removes a transaction, checking that it belongs to the
// caller.
func (s *Server) claimTransaction(token, userID string) error {
	if token == "" {
		return status.Error(codes.InvalidArgument, "no transaction")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	txn, ok := s.txns[token]
	if !ok {
		return status.Error(codes.FailedPrecondition, "unknown or already-finished transaction")
	}
	if txn.userID != userID {
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
	ctx := stream.Context()
	caller, ok := callerFrom(ctx)
	if !ok {
		return errUnauthenticated
	}

	sub := s.watch.subscribe(caller.userID)
	defer s.watch.unsubscribe(sub)

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
			s.handleWatchRequest(sub, req, out)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-done:
			// The console stopped sending. It may still be reading, but there
			// is nothing left to tell it about: its targets went with the
			// stream.
			return nil
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
		}
	}
}

func (s *Server) handleWatchRequest(
	sub *subscriber, req *syncpb.KeepUserSessionRequest,
	out chan<- *syncpb.KeepUserSessionResponse,
) {
	switch r := req.GetRequestType().(type) {
	case *syncpb.KeepUserSessionRequest_Echo:
		// Echoed back verbatim. It is the client's own liveness check, so
		// changing the payload would defeat the only thing it is for.
		send(out, &syncpb.KeepUserSessionResponse{
			ResponseType: &syncpb.KeepUserSessionResponse_Echo{Echo: r.Echo},
		})

	case *syncpb.KeepUserSessionRequest_UpdateTarget:
		target := r.UpdateTarget.GetTarget()
		s.watch.addTarget(sub, target)
		// The client is told the target is watched BEFORE any change for it
		// arrives, so it can tell "nothing has changed" from "my target never
		// took effect".
		send(out, &syncpb.KeepUserSessionResponse{
			ResponseType: &syncpb.KeepUserSessionResponse_TargetChange{
				TargetChange: &syncpb.TargetChange{
					TargetId: target.GetName(),
					// LISTED, not UPDATED: the client is being told the target
					// now exists and is being watched, before any change for it
					// arrives. UPDATED would say a document under it changed,
					// which nothing has yet.
					TargetChangeType: syncpb.TargetChange_LISTED,
				},
			},
		})

	case *syncpb.KeepUserSessionRequest_DeleteTarget:
		name := r.DeleteTarget.GetName()
		s.watch.removeTarget(sub, name)
		send(out, &syncpb.KeepUserSessionResponse{
			ResponseType: &syncpb.KeepUserSessionResponse_TargetChange{
				TargetChange: &syncpb.TargetChange{
					TargetId:         name,
					TargetChangeType: syncpb.TargetChange_DELETED,
				},
			},
		})
	}
}

// send drops a message the writer cannot keep up with, rather than blocking the
// reader goroutine. A console that has stopped reading its own stream must not
// be able to wedge the goroutine reading its requests.
func send(out chan<- *syncpb.KeepUserSessionResponse, msg *syncpb.KeepUserSessionResponse) {
	select {
	case out <- msg:
	default:
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
	if len(paths) == 0 {
		return out
	}

	keep := make(map[string]bool, len(paths))
	for _, p := range paths {
		// Only the first segment. A nested mask would need field-level pruning
		// inside a MapValue, and pruning a path this code does not understand
		// would silently drop data the client asked for. Keeping the whole
		// top-level field errs towards sending too much, which is the harmless
		// direction.
		keep[strings.SplitN(p, ".", 2)[0]] = true
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
