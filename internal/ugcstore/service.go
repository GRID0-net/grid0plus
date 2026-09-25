// Package ugcstore serves nn.npln.ugcstore.v1.Ugcstore: the document store
// Splatoon 3 keeps its own per-player records in.
//
// The game reads its GameRecord documents right after signing in and again on
// entering the lobby, creates the ones that are missing through
// GameRecord/InitializeAttributes (internal/toyohr, via Put below), and reads
// them back. What that round trip needs, from
// docs/CAPTURES/npln-gamerecord-attributes-2026-09-23.md:
//
//   - a document that does not exist is NotFound, sent as headers then a
//     separate trailers frame (the npln interceptor does that for every unary
//     call) -- an empty Document message instead crashes the game;
//   - writes persist, or a document the game created is missing on the next
//     read and it re-initialises forever;
//   - a name that does not parse is never stored.
//
// GetDocument, CommitDocuments and RunQuery are served -- RunQuery always
// with zero matches, see its own doc comment for why that is a real answer
// and not a placeholder. Bulk reads and attachments stay Unimplemented
// until a console asks for them -- the gateway's "unimplemented" log is the
// list.
package ugcstore

import (
	"context"
	"errors"
	"google.golang.org/protobuf/types/known/timestamppb"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/splatdiag"
	commonpb "github.com/n-popescu/switchnet/proto/common"
	ugcpb "github.com/n-popescu/switchnet/proto/ugcstore/v1"
)

// Service is the Ugcstore implementation.
type Service struct {
	ugcpb.UnimplementedUgcstoreServer

	store      db.Store
	log        *slog.Logger
	tenant     string
	callerID   func(context.Context) (string, bool)
	now        func() time.Time
	diagnostic *splatdiag.Bundle
}

// Options configures the service.
type Options struct {
	Store  db.Store
	Log    *slog.Logger
	Tenant string
	// CallerID resolves the authenticated player behind an RPC. Nil makes
	// every call refuse: identity is fail-closed.
	CallerID   func(context.Context) (string, bool)
	Now        func() time.Time
	Diagnostic *splatdiag.Bundle
}

// NewService builds the service.
func NewService(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Service{store: opts.Store, log: opts.Log, tenant: opts.Tenant,
		callerID: opts.CallerID, now: opts.Now, diagnostic: opts.Diagnostic}
}

// Register attaches the service to a gRPC server.
func (s *Service) Register(srv grpc.ServiceRegistrar) { ugcpb.RegisterUgcstoreServer(srv, s) }

func (s *Service) caller(ctx context.Context) (string, error) {
	if s.callerID == nil {
		return "", status.Error(codes.Unauthenticated, "unauthenticated")
	}
	id, ok := s.callerID(ctx)
	if !ok || id == "" {
		return "", status.Error(codes.Unauthenticated, "unauthenticated")
	}
	return id, nil
}

// GetDocument returns one document by name.
func (s *Service) GetDocument(ctx context.Context, req *ugcpb.GetDocumentRequest) (*ugcpb.Document, error) {
	uid, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	key, ok := s.canonicalName(req.GetName())
	if !ok {
		s.log.Warn("ugcstore: unparseable document name", "name", req.GetName())
		return nil, status.Error(codes.InvalidArgument, "invalid document name")
	}
	d, err := s.store.GetUGCDocument(ctx, key)
	switch {
	case errors.Is(err, db.ErrNotFound):
		if doc, enabled := s.diagnostic.AttributeFor(uid, key, s.now()); enabled {
			s.log.Info("ugcstore: private diagnostic attribute served", "name", key)
			return doc, nil
		}
		if attrs, ok := startingAttributes(key, uid); ok {
			doc, err := s.Put(ctx, uid, req.GetName(), attrs)
			if err != nil {
				return nil, err
			}
			s.log.Info("ugcstore: created a starting attribute document", "name", key)
			return doc, nil
		}
		if emptyWhenAbsent(key, uid) {
			now := timestamppb.New(s.now())
			s.log.Info("ugcstore: absent GameRecord document answered named and empty", "name", key)
			return &ugcpb.Document{Name: req.GetName(),
				Fields:     &commonpb.MapValue{Fields: map[string]*commonpb.Value{}},
				CreateTime: now, UpdateTime: now}, nil
		}
		s.log.Info("ugcstore: requested document is absent", "name", key)
		return nil, missingDocument(req.GetName())
	case err != nil:
		s.log.Error("ugcstore: load document", "name", key, "error", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	doc, err := toProto(d, req.GetName())
	if err != nil {
		s.log.Error("ugcstore: decode document", "name", key, "error", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	return doc, nil
}

// RunQuery answers every structured query with zero matches, never
// Unimplemented.
//
// Confirmed against a real capture (docs/CAPTURES/raw's Nextendo second
// session, RunQuery against a live Splatoon 3 account, PolyForm
// Shield-licensed source -- facts only, no code taken): a real server's
// response to a query with no matches is exactly one streamed
// RunQueryResponse carrying only read_time, with document and
// skipped_results left unset. That is what SwitchNet answers here --
// SwitchNet keeps no document collections a StructuredQuery could actually
// match against (GetDocument/CommitDocuments address one document by name,
// never a collection), so "zero matches, structurally correct" is not a
// placeholder, it is this server's honest answer. Reproduced on real
// hardware on 2026-09-24: Unimplemented here (grpc-status 12) is what the
// console reads as a communication error right after entering the lobby --
// this was the last RPC in the confirmed boot sequence still unanswered.
func (s *Service) RunQuery(
	req *ugcpb.RunQueryRequest, stream grpc.ServerStreamingServer[ugcpb.RunQueryResponse],
) error {
	if _, err := s.caller(stream.Context()); err != nil {
		return err
	}
	return stream.Send(&ugcpb.RunQueryResponse{
		ReadTime: timestamppb.New(s.now()),
	})
}

// pending is one document's state while a commit is being applied.
type pending struct {
	existed bool
	deleted bool
	row     db.UGCDocument
	fields  *commonpb.MapValue
	dirty   bool
}

// CommitDocuments applies a batch of writes, all or nothing.
//
// Every operation is checked and applied to a working copy first; storage is
// touched only once the whole batch is known to be valid, so a refused
// operation leaves nothing half-written.
func (s *Service) CommitDocuments(ctx context.Context, req *ugcpb.CommitDocumentsRequest) (*ugcpb.CommitDocumentsResponse, error) {
	uid, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	work := map[string]*pending{}
	var order []string

	load := func(name string) (*pending, error) {
		key, ok := s.canonicalName(name)
		if !ok {
			s.log.Warn("ugcstore: unparseable document name in commit", "name", name, "user", uid)
			return nil, status.Error(codes.InvalidArgument, "invalid document name")
		}
		if p, ok := work[key]; ok {
			return p, nil
		}
		p := &pending{row: db.UGCDocument{Name: key, Owner: uid, CreateTime: now}}
		d, err := s.store.GetUGCDocument(ctx, key)
		switch {
		case err == nil:
			p.existed, p.row = true, d
			if p.fields, err = decodeFields(d.Fields); err != nil {
				s.log.Error("ugcstore: decode document", "name", key, "error", err)
				return nil, status.Error(codes.Internal, "internal error")
			}
		case errors.Is(err, db.ErrNotFound):
			p.fields = &commonpb.MapValue{Fields: map[string]*commonpb.Value{}}
		default:
			s.log.Error("ugcstore: load document", "name", key, "error", err)
			return nil, status.Error(codes.Internal, "internal error")
		}
		if err := s.mayWrite(uid, p); err != nil {
			return nil, err
		}
		work[key] = p
		order = append(order, key)
		return p, nil
	}

	results := make([]*ugcpb.WriteResult, 0, len(req.GetWriteOperations()))
	for _, op := range req.GetWriteOperations() {
		res := &ugcpb.WriteResult{UpdateTime: timestamppb.New(now)}
		switch w := op.GetOperationType().(type) {
		case *ugcpb.WriteOperation_UpdateDocument:
			p, err := load(w.UpdateDocument.GetDocument().GetName())
			if err != nil {
				return nil, err
			}
			if err := checkPrecondition(p, w.UpdateDocument.GetCurrentDocument()); err != nil {
				return nil, err
			}
			if err := applyUpdate(p, w.UpdateDocument); err != nil {
				return nil, status.Error(codes.InvalidArgument, err.Error())
			}
		case *ugcpb.WriteOperation_DeleteDocument:
			p, err := load(w.DeleteDocument.GetName())
			if err != nil {
				return nil, err
			}
			if err := checkPrecondition(p, w.DeleteDocument.GetCurrentDocument()); err != nil {
				return nil, err
			}
			p.deleted, p.dirty = true, true
			p.fields = &commonpb.MapValue{Fields: map[string]*commonpb.Value{}}
		case *ugcpb.WriteOperation_TransformDocument:
			p, err := load(w.TransformDocument.GetName())
			if err != nil {
				return nil, err
			}
			for _, t := range w.TransformDocument.GetFieldTransforms() {
				v, err := applyTransform(p.fields, t, now)
				if err != nil {
					return nil, status.Error(codes.InvalidArgument, err.Error())
				}
				res.TransformResults = append(res.TransformResults, v)
			}
			p.deleted, p.dirty = false, true
		default:
			// Attachments: not implemented. Refusing the whole batch is
			// what "all or nothing" requires.
			s.log.Info("ugcstore: unsupported write operation", "type", op.GetOperationType())
			return nil, status.Error(codes.Unimplemented, "write operation not implemented")
		}
		results = append(results, res)
	}

	for _, key := range order {
		p := work[key]
		if !p.dirty {
			continue
		}
		if p.deleted {
			if p.existed {
				if err := s.store.DeleteUGCDocument(ctx, key); err != nil && !errors.Is(err, db.ErrNotFound) {
					s.log.Error("ugcstore: delete document", "name", key, "error", err)
					return nil, status.Error(codes.Internal, "internal error")
				}
			}
			continue
		}
		b, err := encodeFields(p.fields)
		if err != nil {
			return nil, status.Error(codes.Internal, "internal error")
		}
		p.row.Fields, p.row.UpdateTime = b, now
		if err := s.store.PutUGCDocument(ctx, p.row); err != nil {
			s.log.Error("ugcstore: store document", "name", key, "error", err)
			return nil, status.Error(codes.Internal, "internal error")
		}
	}
	return &ugcpb.CommitDocumentsResponse{WriteResults: results, CommitTime: timestamppb.New(now)}, nil
}

// mayWrite enforces who may change a document: the player a users/<id> path
// names, and otherwise whoever created it.
func (s *Service) mayWrite(uid string, p *pending) error {
	if owner, ok := pathOwner(p.row.Name); ok && owner != uid {
		s.log.Warn("ugcstore: write to another player's document refused",
			"user", uid, "name", p.row.Name)
		return status.Error(codes.PermissionDenied, "permission denied")
	}
	if p.existed && p.row.Owner != uid {
		s.log.Warn("ugcstore: write to a document created by someone else refused",
			"user", uid, "owner", p.row.Owner, "name", p.row.Name)
		return status.Error(codes.PermissionDenied, "permission denied")
	}
	return nil
}

func checkPrecondition(p *pending, c *ugcpb.Precondition) error {
	// Exists now: stored and not deleted in this batch, or created earlier in it.
	exists := !p.deleted && (p.existed || p.dirty)
	switch cond := c.GetConditionType().(type) {
	case *ugcpb.Precondition_Exists:
		if cond.Exists != exists {
			return status.Error(codes.FailedPrecondition, "document existence precondition failed")
		}
	case *ugcpb.Precondition_UpdateTime:
		if !exists || !p.row.UpdateTime.Equal(cond.UpdateTime.AsTime()) {
			return status.Error(codes.FailedPrecondition, "document update time precondition failed")
		}
	}
	return nil
}

// applyUpdate writes an UpdateDocumentRequest into p. Without a mask the
// document's fields are replaced; with one, only the masked paths change --
// set from the request's document if present there, removed if not.
func applyUpdate(p *pending, u *ugcpb.UpdateDocumentRequest) error {
	incoming := u.GetDocument().GetFields()
	if incoming == nil {
		incoming = &commonpb.MapValue{}
	}
	mask := u.GetUpdateMask().GetPaths()
	if len(mask) == 0 {
		p.fields = proto.Clone(incoming).(*commonpb.MapValue)
		if p.fields.Fields == nil {
			p.fields.Fields = map[string]*commonpb.Value{}
		}
	} else {
		for _, path := range mask {
			parts, err := splitPath(path)
			if err != nil {
				return err
			}
			v := getPath(incoming, parts)
			if v != nil {
				v = proto.Clone(v).(*commonpb.Value)
			}
			setPath(p.fields, parts, v)
		}
	}
	p.deleted, p.dirty = false, true
	return nil
}

func toProto(d db.UGCDocument, name string) (*ugcpb.Document, error) {
	fields, err := decodeFields(d.Fields)
	if err != nil {
		return nil, err
	}
	return &ugcpb.Document{
		Name:       name,
		Fields:     fields,
		CreateTime: timestamppb.New(d.CreateTime),
		UpdateTime: timestamppb.New(d.UpdateTime),
	}, nil
}

// Put creates or merges a document on behalf of uid, for another service --
// GameRecord/InitializeAttributes -- that creates documents the console then
// reads through this one. Fields already present are kept unless attrs sets
// them. It returns the document as stored.
func (s *Service) Put(ctx context.Context, uid, name string, attrs *commonpb.MapValue) (*ugcpb.Document, error) {
	key, ok := s.canonicalName(name)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "invalid document name")
	}
	now := s.now().UTC()
	p := &pending{row: db.UGCDocument{Name: key, Owner: uid, CreateTime: now},
		fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{}}}
	d, err := s.store.GetUGCDocument(ctx, key)
	switch {
	case err == nil:
		p.existed, p.row = true, d
		if p.fields, err = decodeFields(d.Fields); err != nil {
			return nil, status.Error(codes.Internal, "internal error")
		}
	case !errors.Is(err, db.ErrNotFound):
		return nil, status.Error(codes.Internal, "internal error")
	}
	if err := s.mayWrite(uid, p); err != nil {
		return nil, err
	}
	for k, v := range attrs.GetFields() {
		p.fields.Fields[k] = proto.Clone(v).(*commonpb.Value)
	}
	b, err := encodeFields(p.fields)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	p.row.Fields, p.row.UpdateTime = b, now
	if err := s.store.PutUGCDocument(ctx, p.row); err != nil {
		s.log.Error("ugcstore: store document", "name", key, "error", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	return toProto(p.row, name)
}

// attributeSeason is the season GameRecord's Vs attributes start in: the
// SeasonSchedule id both working hall captures carry (unk2/unk3 = 16), and
// the season_id of the working capture's VsUserAttribute.
const attributeSeason = 16

// startingAttributes answers whether key is one of the caller's own two
// GameRecord attribute documents, and if so what it starts with.
//
// NextendoNetwork/splatoon-3 measured (facts only, not its code) two real
// Nintendo sessions: a console whose GetSaveRecord returned a record reads
// CoopUserAttribute/Data then VsUserAttribute/Data first and gets both --
// it never calls InitializeAttributes; a console with no record gets both
// empty and calls InitializeAttributes. SwitchNet always answers a record
// (see internal/toyohr/cloud_save.go), so the console takes the first path,
// and on 2026-09-25 a NotFound there was followed by 2321-0384. So the
// documents are created on first read, with exactly what
// GameRecord/InitializeAttributes would have stored: the season for Vs,
// nothing for Coop. The game keeps its own default for every absent key.
func startingAttributes(key, uid string) (*commonpb.MapValue, bool) {
	owner, ok := pathOwner(key)
	if !ok || owner != uid {
		return nil, false
	}
	_, docPath, _ := strings.Cut(key, documentsSegment)
	switch docPath {
	case "services/GameRecord/users/" + uid + "/VsUserAttribute/Data":
		return &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"season_id": intValue(attributeSeason),
		}}, true
	case "services/GameRecord/users/" + uid + "/CoopUserAttribute/Data":
		return &commonpb.MapValue{Fields: map[string]*commonpb.Value{}}, true
	}
	return nil, false
}

// emptyWhenAbsent reports whether an absent document is answered as a named,
// empty document instead of NotFound: the caller's own GameRecord documents
// (WeaponPowerMeasurement/<season>, PointCard*, BankaraChallenges/<id>).
//
// NextendoNetwork/splatoon-3's notes (facts only, not its code) record that
// its working boot answers these families this way, and that NotFound on
// WeaponPowerMeasurement/16 made the game loop re-reading it. On hardware on
// 2026-09-25 the game read WeaponPowerMeasurement/16 twice, got NotFound,
// and stopped with 2000-0000. Hammer documents keep NotFound, which the same
// console already gets past. Nothing is stored: the document is not created,
// only reported empty.
func emptyWhenAbsent(key, uid string) bool {
	_, docPath, _ := strings.Cut(key, documentsSegment)
	return strings.HasPrefix(docPath, "services/GameRecord/users/"+uid+"/")
}
