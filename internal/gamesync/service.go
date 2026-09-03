// Package gamesync implements nn.npln.gamesync.v1.Gamesync, the Firestore-
// shaped document/watch service Splatoon 3 reads and writes lobby and match
// state through.
//
// Registered on internal/npln's own *grpc.Server, the same way
// internal/toyohr is (internal/npln/services.go): gamesync.npln.nintendo.net
// is classified ServiceNPLN in internal/nintendo/hosts.go and routed by the
// nginx generator to the same upstream as the tenant gRPC host, not to a
// separate port -- see TODO.md §1.5 for the evidence and what would change
// it. Because of that, this package cannot import internal/npln (which
// registers it) without a cycle, so it reads the caller's identity from
// internal/rpcauth the same way internal/toyohr does; see that package's
// comment for why that is the seam rather than a second authentication path.
//
// The document store itself lives in internal/gamesyncstore -- this package
// is only the RPC-to-store translation, plus the parts of the protocol a
// generic document store cannot answer on its own: minting this service's
// own session tokens, and pushing live changes down KeepUserSession
// (keep_user_session.go).
package gamesync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/switchnet/internal/gamesyncstore"
	"github.com/n-popescu/switchnet/internal/rpcauth"
	v1 "github.com/n-popescu/switchnet/proto/gamesync/v1"
)

// tokenTTL is how long an access token IssueToken/RefreshToken hands back is
// valid for, and refreshTokenTTL how long the refresh token behind it can be
// redeemed. Neither token is checked anywhere else in this package: every
// Gamesync RPC, this pair included, is already behind internal/npln's own
// NPLN access token requirement, so nothing downstream of IssueToken depends
// on these values being right. They exist because IssueTokenResponse.Token
// is not optional in the wire shape, not because SwitchNet has anywhere else
// to spend them -- see the package doc and README for the reasoning.
const (
	tokenTTL        = 3 * time.Hour
	refreshTokenTTL = 30 * 24 * time.Hour
)

// Service implements v1.GamesyncServer. CreateRound is left to
// UnimplementedGamesyncServer: proto/gamesync/v1/resources.pb.go's own
// Round message names its fields Unk1 through Unk4, so even this project's
// own proto reversal has nothing defensible to fill in. See
// docs/CAPTURES/README.md.
type Service struct {
	v1.UnimplementedGamesyncServer

	store *gamesyncstore.Store
	log   *slog.Logger
	now   func() time.Time

	mu      sync.Mutex
	refresh map[string]refreshRecord
}

type refreshRecord struct {
	userID    string
	expiresAt time.Time
}

// NewService builds a Service. now is overridable in tests.
func NewService(store *gamesyncstore.Store, log *slog.Logger, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:   store,
		log:     log,
		now:     now,
		refresh: map[string]refreshRecord{},
	}
}

// Register attaches the service to a gRPC server.
func (s *Service) Register(g *grpc.Server) {
	v1.RegisterGamesyncServer(g, s)
}

// IssueToken mints a Gamesync session for the already-authenticated NPLN
// caller.
//
// The request's own UserSession and MatchmakingIdToken fields name a session
// rather than prove one -- the NPLN access token that already authenticated
// this call is what proves identity, the same distinction
// nn.npln.auth.v1.Auth.IssueToken's own request makes about its `user`
// field (internal/npln/services.go).
func (s *Service) IssueToken(ctx context.Context, _ *v1.IssueTokenRequest) (*v1.IssueTokenResponse, error) {
	caller, ok := rpcauth.From(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	return &v1.IssueTokenResponse{Token: s.mint(caller.UserID)}, nil
}

// RefreshToken exchanges a refresh token minted by IssueToken for a new
// Gamesync session, for the same caller.
func (s *Service) RefreshToken(ctx context.Context, req *v1.RefreshTokenRequest) (*v1.RefreshTokenResponse, error) {
	caller, ok := rpcauth.From(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}

	s.mu.Lock()
	rec, ok := s.refresh[req.GetRefreshToken()]
	if ok {
		delete(s.refresh, req.GetRefreshToken())
	}
	s.mu.Unlock()

	if !ok || rec.userID != caller.UserID || s.now().After(rec.expiresAt) {
		return nil, status.Error(codes.Unauthenticated, "unknown or expired refresh token")
	}
	return &v1.RefreshTokenResponse{Token: s.mint(caller.UserID)}, nil
}

func (s *Service) mint(userID string) *v1.Token {
	refreshToken := randomToken()
	s.mu.Lock()
	s.refresh[refreshToken] = refreshRecord{userID: userID, expiresAt: s.now().Add(refreshTokenTTL)}
	s.mu.Unlock()

	return &v1.Token{
		UserSession:  userID,
		AccessToken:  randomToken(),
		RefreshToken: refreshToken,
		Ttl:          durationpb.New(tokenTTL),
	}
}

func randomToken() string {
	var raw [24]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

// GetDocument reads one document.
func (s *Service) GetDocument(ctx context.Context, req *v1.GetDocumentRequest) (*v1.Document, error) {
	if _, ok := rpcauth.From(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	doc := s.store.Get(req.GetName())
	if doc == nil {
		return nil, status.Errorf(codes.NotFound, "document not found: %s", req.GetName())
	}
	return applyReadMask(doc, req.GetReadMask()), nil
}

// ReadDocuments reads a batch of documents, distinguishing found from
// missing the way ReadResult's own oneof does -- a missing document is not
// an error for this RPC, only for GetDocument's single-document form.
func (s *Service) ReadDocuments(ctx context.Context, req *v1.ReadDocumentsRequest) (*v1.ReadDocumentsResponse, error) {
	if _, ok := rpcauth.From(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	names := req.GetDocuments()
	docs := s.store.Read(names)
	results := make([]*v1.ReadResult, len(docs))
	for i, doc := range docs {
		if doc == nil {
			results[i] = &v1.ReadResult{Result: &v1.ReadResult_Missing{Missing: names[i]}}
			continue
		}
		results[i] = &v1.ReadResult{Result: &v1.ReadResult_Found{Found: applyReadMask(doc, req.GetReadMask())}}
	}
	return &v1.ReadDocumentsResponse{Results: results, ReadTime: timestamppb.New(s.now())}, nil
}

// ListDocuments lists the documents directly under parent.
//
// ShowMissing is not honoured: it asks for placeholder entries representing
// a document that does not itself exist but has children, and this store has
// no notion of a document existing independently of having been written --
// see internal/gamesyncstore's README.
func (s *Service) ListDocuments(ctx context.Context, req *v1.ListDocumentsRequest) (*v1.ListDocumentsResponse, error) {
	if _, ok := rpcauth.From(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	docs, next := s.store.List(req.GetParent(), req.GetPageSize(), req.GetPageToken())
	for i, doc := range docs {
		docs[i] = applyReadMask(doc, req.GetReadMask())
	}
	return &v1.ListDocumentsResponse{Documents: docs, NextPageToken: next}, nil
}

// QueryCollectionIds lists the distinct collection IDs directly under
// document.
func (s *Service) QueryCollectionIds(ctx context.Context, req *v1.QueryCollectionIdsRequest) (*v1.QueryCollectionIdsResponse, error) {
	if _, ok := rpcauth.From(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	ids := s.store.CollectionIDs(req.GetDocument())

	start := 0
	if token := req.GetPageToken(); token != "" {
		for i, id := range ids {
			if id == token {
				start = i + 1
				break
			}
		}
	}
	ids = ids[start:]

	var next string
	if size := req.GetPageSize(); size > 0 && int(size) < len(ids) {
		next = ids[size-1]
		ids = ids[:size]
	}
	return &v1.QueryCollectionIdsResponse{CollectionIds: ids, NextPageToken: next}, nil
}

// WriteDocuments applies a batch of write operations, non-transactionally --
// each one lands (or fails) independently, unlike CommitTransaction's
// all-or-nothing batch.
func (s *Service) WriteDocuments(ctx context.Context, req *v1.WriteDocumentsRequest) (*v1.WriteDocumentsResponse, error) {
	if _, ok := rpcauth.From(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	s.logDeferments(len(req.GetDefermentOperations()))

	results := make([]*v1.WriteResult, len(req.GetWriteOperations()))
	for i, op := range req.GetWriteOperations() {
		res, err := s.applyWrite(op)
		if err != nil {
			return nil, err
		}
		results[i] = res
	}
	return &v1.WriteDocumentsResponse{WriteResults: results, CommitTime: timestamppb.New(s.now())}, nil
}

func (s *Service) applyWrite(op *v1.WriteOperation) (*v1.WriteResult, error) {
	switch t := op.GetOperationType().(type) {
	case *v1.WriteOperation_UpdateDocument:
		if _, err := s.store.Update(t.UpdateDocument.GetDocument(), t.UpdateDocument.GetUpdateMask(), t.UpdateDocument.GetCurrentDocument()); err != nil {
			return nil, err
		}
		return &v1.WriteResult{ResultType: &v1.WriteResult_UpdateResult{UpdateResult: &v1.UpdateResult{}}}, nil
	case *v1.WriteOperation_MergeDocument:
		if _, err := s.store.Merge(t.MergeDocument.GetDocument()); err != nil {
			return nil, err
		}
		return &v1.WriteResult{ResultType: &v1.WriteResult_UpdateResult{UpdateResult: &v1.UpdateResult{}}}, nil
	case *v1.WriteOperation_TransformDocument:
		_, results, err := s.store.Transform(t.TransformDocument.GetName(), t.TransformDocument.GetFieldTransforms())
		if err != nil {
			return nil, err
		}
		return &v1.WriteResult{ResultType: &v1.WriteResult_TransformResult{TransformResult: &v1.TransformResult{Results: results}}}, nil
	case *v1.WriteOperation_DeleteDocument:
		if err := s.store.Delete(t.DeleteDocument.GetName(), t.DeleteDocument.GetCurrentDocument()); err != nil {
			return nil, err
		}
		return &v1.WriteResult{ResultType: &v1.WriteResult_DeleteResult{DeleteResult: &v1.DeleteResult{}}}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "no write operation given")
	}
}

// LazyWriteDocuments applies merge and transform operations best-effort.
//
// LazyWriteConfig is opaque here -- nothing found documents what
// configuration a "lazy write" takes, so it is accepted and ignored rather
// than rejecting the call over a field this project cannot interpret.
func (s *Service) LazyWriteDocuments(ctx context.Context, req *v1.LazyWriteDocumentsRequest) (*v1.LazyWriteDocumentsResponse, error) {
	if _, ok := rpcauth.From(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	for _, op := range req.GetWriteOperations() {
		switch t := op.GetOperationType().(type) {
		case *v1.LazyWriteOperation_MergeDocument:
			if _, err := s.store.Merge(t.MergeDocument.GetDocument()); err != nil {
				return nil, err
			}
		case *v1.LazyWriteOperation_TransformDocument:
			if _, _, err := s.store.Transform(t.TransformDocument.GetName(), t.TransformDocument.GetFieldTransforms()); err != nil {
				return nil, err
			}
		}
	}
	return &v1.LazyWriteDocumentsResponse{CommitTime: timestamppb.New(s.now())}, nil
}

// BeginTransaction opens a transaction. See internal/gamesyncstore's README
// for what this service's transactions do and do not guarantee.
func (s *Service) BeginTransaction(ctx context.Context, _ *v1.BeginTransactionRequest) (*v1.BeginTransactionResponse, error) {
	if _, ok := rpcauth.From(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	return &v1.BeginTransactionResponse{Transaction: s.store.BeginTransaction()}, nil
}

// CommitTransaction applies a transaction's writes atomically.
func (s *Service) CommitTransaction(ctx context.Context, req *v1.CommitTransactionRequest) (*v1.CommitTransactionResponse, error) {
	if _, ok := rpcauth.From(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	s.logDeferments(len(req.GetDefermentOperations()))

	results, err := s.store.CommitTransaction(req.GetTransaction(), req.GetWriteOperations())
	if err != nil {
		return nil, err
	}
	return &v1.CommitTransactionResponse{WriteResults: results, CommitTime: timestamppb.New(s.now())}, nil
}

// RollbackTransaction discards a transaction opened by BeginTransaction.
func (s *Service) RollbackTransaction(ctx context.Context, req *v1.RollbackTransactionRequest) (*emptypb.Empty, error) {
	if _, ok := rpcauth.From(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	s.store.RollbackTransaction(req.GetTransaction())
	return &emptypb.Empty{}, nil
}

func (s *Service) logDeferments(n int) {
	if n == 0 {
		return
	}
	// TODO(capture-needed): DefermentOperation has no Firestore equivalent
	// and no public documentation of what a "deferment" defers, or until
	// when. Logged rather than silently dropped, so a real console sending
	// one shows up here the same way an Unimplemented RPC's log line does
	// for internal/toyohr's backlog.
	s.log.Debug("gamesync: deferment operations ignored (undocumented shape)", "count", n)
}

// applyReadMask filters doc's fields down to mask's paths, or returns doc
// unchanged if mask names nothing -- the same "empty mask means everything"
// convention Update's own field mask uses.
func applyReadMask(doc *v1.Document, mask *fieldmaskpb.FieldMask) *v1.Document {
	if doc == nil || mask == nil || len(mask.GetPaths()) == 0 {
		return doc
	}
	return &v1.Document{
		Name:       doc.GetName(),
		Fields:     gamesyncstore.FilterFields(doc.GetFields(), mask.GetPaths()),
		CreateTime: doc.GetCreateTime(),
		UpdateTime: doc.GetUpdateTime(),
	}
}
