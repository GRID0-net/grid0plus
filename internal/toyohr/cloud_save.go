package toyohr

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/switchnet/proto/common"
	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// registerCloudSave attaches CloudSave with GetSaveRecord switched to a
// hand-rolled stream, the same technique internal/ugcstore uses for
// GetDocument: a unary handler that returns (nil, nil) still has grpc-go
// marshal and send a zero-length DATA frame (5 bytes of gRPC framing, no
// payload) -- there is no way to send truly nothing through it. Only a
// method registered as a stream can end the call in OK status without ever
// calling SendMsg. Every other CloudSave method keeps the generated
// unary dispatch.
func registerCloudSave(srv *grpc.Server, impl *Service) {
	desc := toyohrpb.CloudSave_ServiceDesc // copy

	methods := make([]grpc.MethodDesc, 0, len(desc.Methods))
	for _, m := range desc.Methods {
		if m.MethodName == "GetSaveRecord" {
			continue // reattached below, as a stream
		}
		methods = append(methods, m)
	}
	desc.Methods = methods

	streams := make([]grpc.StreamDesc, 0, len(desc.Streams)+1)
	streams = append(streams, desc.Streams...)
	streams = append(streams, grpc.StreamDesc{
		StreamName:    "GetSaveRecord",
		Handler:       getSaveRecordStreamHandler,
		ServerStreams: true,
	})
	desc.Streams = streams

	srv.RegisterService(&desc, impl)
}

// getSaveRecordStreamHandler answers CloudSave/GetSaveRecord.
//
// NextendoNetwork/splatoon-3 measured (facts only, not its code) this
// exact RPC against real Nintendo servers TWICE, for a Nintendo account with
// nothing in the cloud: "GetSaveRecord Succeeded, request 42B -> response
// 0B" -- no message at all, not even an empty one, framed as ordinary
// headers (content-length: 0) followed by a separate trailers frame
// carrying grpc-status OK (never Trailers-Only). The console read that as
// "nothing in the cloud" and, on a genuinely fresh account, answered with
// CreateSaveRecord uploading its own local save 6 seconds later; on an
// account already past that point, it answered with a WriteSaveRecord
// evt=Validate carrying exactly the four keys a blank record is missing
// (CloudRandomSet, HaveGearClothesMap, HaveGearHeadMap, HaveGearShoesMap) --
// which is answered, not left Unimplemented, see WriteSaveRecord's own
// doc comment.
//
// Two other shapes were tried and measured broken, and SwitchNet must not
// regress to either:
//   - NotFound: the game creates nothing and loops on a communication
//     error (UserScreening/GetViolation's own doc comment covers why an
//     absent resource is still answered NotFound there instead -- the two
//     RPCs measured differently, this one specifically did not tolerate it);
//   - a real message whose SaveData is merely an empty MapValue (SwitchNet's
//     own previous answer here, and the direct cause of a 2026-09-24
//     hardware session where the console went silent right after this call,
//     never issuing FestSchedule, the early GameRecord document reads, or
//     WriteSaveRecord): a message, even an empty one, still carries the
//     5-byte gRPC frame header real Nintendo's zero-byte response does not.
//     The console's own SDK reads "a record that exists and is empty"
//     differently from "nothing in the cloud".
//
// Once the console has uploaded anything -- CreateSaveRecord's full record or
// a WriteSaveRecord delta -- that stored record is what it gets back here:
// NextendoNetwork/splatoon-3 measured that a resolved record is what lets
// the game go on to SelectFestSchedule, the early GameRecord document reads
// and further WriteSaveRecord calls, where "nothing in the cloud" leaves an
// established console stopped. Only a player with nothing stored still gets
// the zero-message answer.
func getSaveRecordStreamHandler(srv any, stream grpc.ServerStream) error {
	s, ok := srv.(*Service)
	if !ok {
		return status.Error(codes.Internal, "wrong service type")
	}

	req := new(toyohrpb.GetSaveRecordRequest)
	if err := stream.RecvMsg(req); err != nil {
		return err
	}

	ctx := stream.Context()
	uid, ok := s.caller(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "")
	}

	if s.diagnostic != nil && s.diagnostic.UserID == uid {
		rec, err := s.diagnosticSaveRecord(ctx, uid)
		if err != nil {
			return err
		}
		return stream.SendMsg(rec)
	}

	rec, err := s.loadSave(ctx, uid)
	if err != nil {
		s.log.Error("toyohr: load cloud save", "user", uid, "error", err)
		return status.Error(codes.Internal, "internal error")
	}
	if rec != nil {
		s.log.Info("toyohr: GetSaveRecord answered with the stored record",
			"user", uid, "keys", len(rec.GetSaveData().GetFields()))
		return stream.SendMsg(rec)
	}

	if err := stream.SendHeader(metadata.Pairs("npln-grpc-type", "Unary", "content-length", "0")); err != nil {
		return err
	}
	s.log.Info("toyohr: GetSaveRecord answered with no cloud save", "user", uid)
	return nil
}

// diagnosticSaveRecord builds the private diagnostic probe's own resolved
// save record. Kept separate from getSaveRecordStreamHandler so the
// diagnostic path -- which does need a real message, unlike every other
// caller -- stays easy to find and does not get lost in the wire plumbing
// above.
func (s *Service) diagnosticSaveRecord(ctx context.Context, uid string) (*toyohrpb.SaveRecord, error) {
	user, err := s.store.GetUser(ctx, uid)
	if err != nil {
		return nil, status.Error(codes.Internal, "diagnostic user unavailable")
	}
	rec, _ := s.diagnostic.SaveFor(uid, s.tenant, user.Nickname, s.now())
	s.log.Info("toyohr: private diagnostic save served", "keys", len(rec.GetSaveData().GetFields()))
	return rec, nil
}

// GetSaveRecord is the direct, non-wire entry point registerCloudSave's
// stream handler bypasses for every real console. It still exists for
// callers that invoke the Go method directly rather than over gRPC (tests,
// and internal/npln's diagnostic probe), and still returns a real,
// structurally-complete record -- see diagnosticSaveRecord and
// getSaveRecordStreamHandler's own doc comment above for what a real
// console actually receives on the wire, and why it differs from this.
func (s *Service) GetSaveRecord(
	ctx context.Context, _ *toyohrpb.GetSaveRecordRequest,
) (*toyohrpb.SaveRecord, error) {
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	if s.diagnostic != nil && s.diagnostic.UserID == uid {
		return s.diagnosticSaveRecord(ctx, uid)
	}
	if rec, err := s.loadSave(ctx, uid); err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	} else if rec != nil {
		return rec, nil
	}
	now := timestamppb.New(s.now())
	return &toyohrpb.SaveRecord{
		Name:       s.saveRecordName(uid),
		SaveData:   &common.MapValue{Fields: map[string]*common.Value{}},
		CreateTime: now,
		UpdateTime: now,
	}, nil
}

// WriteSaveRecord merges a delta into the player's stored record, creating
// the record if this is the first thing the console has sent.
//
// The console writes right after GetSaveRecord: an established console that
// is told "nothing in the cloud" sends evt=Validate carrying the keys a blank
// record lacks (the 2026-09-24 loopback capture shows HaveGearClothesMap,
// HaveGearHeadMap and HaveGearShoesMap, 34 KB, twice), and
// NextendoNetwork/splatoon-3 documents the events that follow once those are
// accepted (InitFest, InitCoopTeamContest, NewSeason, SeqFlagVsLobbyMulti).
// Discarding them, as this used to, handed the same empty save back on
// every boot, so the console never had a resolved record.
//
// The response's timestamp is the record's new update_time, which the
// console presents as the next write's version token.
//
// While the private diagnostic serves this player a captured record, writes
// are acknowledged and not stored: they are deltas against someone else's
// progression.
func (s *Service) WriteSaveRecord(
	ctx context.Context, req *toyohrpb.WriteSaveRecordRequest,
) (*toyohrpb.WriteSaveRecordResponse, error) {
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	event := strings.TrimPrefix(req.GetSaveEventType(), "tenants/current/saveEventTypes/")
	if s.diagnostic != nil && s.diagnostic.UserID == uid {
		return &toyohrpb.WriteSaveRecordResponse{Timestamp: timestamppb.New(s.now())}, nil
	}

	unlock := lockSave(uid)
	defer unlock()
	rec, err := s.loadSave(ctx, uid)
	if err != nil {
		s.log.Error("toyohr: load cloud save", "user", uid, "error", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	created := rec == nil
	if created {
		now := timestamppb.New(s.now())
		rec = &toyohrpb.SaveRecord{
			Name:       s.saveRecordName(uid),
			SaveData:   &common.MapValue{Fields: map[string]*common.Value{}},
			CreateTime: now,
		}
	}
	mergeSaveDelta(rec.SaveData, req.GetSaveRecord().GetSaveData())
	rec.UpdateTime = nextSaveVersion(s.now(), rec.GetUpdateTime())
	if err := s.putSave(ctx, uid, rec); err != nil {
		s.log.Error("toyohr: store cloud save", "user", uid, "error", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	s.log.Info("toyohr: WriteSaveRecord stored", "user", uid, "event", event,
		"delta_keys", len(req.GetSaveRecord().GetSaveData().GetFields()),
		"record_keys", len(rec.SaveData.Fields), "created", created)
	return &toyohrpb.WriteSaveRecordResponse{Timestamp: rec.UpdateTime}, nil
}

// CreateSaveRecord stores the record a console builds from its own local
// save and answers with it. NextendoNetwork/splatoon-3 measured this
// against real Nintendo for a fresh account: GetSaveRecord with nothing in
// the cloud, then CreateSaveRecord uploading ~15 KB six seconds later, then
// WriteSaveRecord. The record is filed under the caller's own name whatever
// name the request carries.
func (s *Service) CreateSaveRecord(
	ctx context.Context, req *toyohrpb.CreateSaveRecordRequest,
) (*toyohrpb.SaveRecord, error) {
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	unlock := lockSave(uid)
	defer unlock()

	rec := &toyohrpb.SaveRecord{
		Name:     s.saveRecordName(uid),
		SaveData: &common.MapValue{Fields: map[string]*common.Value{}},
	}
	mergeSaveDelta(rec.SaveData, req.GetSaveRecord().GetSaveData())
	prev, err := s.loadSave(ctx, uid)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	now := timestamppb.New(s.now())
	rec.CreateTime = now
	var prevVersion *timestamppb.Timestamp
	if prev != nil {
		prevVersion = prev.GetUpdateTime()
	}
	rec.UpdateTime = nextSaveVersion(s.now(), prevVersion)
	if err := s.putSave(ctx, uid, rec); err != nil {
		s.log.Error("toyohr: store cloud save", "user", uid, "error", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	s.log.Info("toyohr: CreateSaveRecord stored", "user", uid,
		"record_keys", len(rec.SaveData.Fields), "replaced", prev != nil)
	return rec, nil
}

// ValidateSaveRecord is acknowledged. It has not appeared in any capture;
// its response has no fields, so there is nothing to invent, and refusing it
// would block a boot on a check this server cannot perform.
func (s *Service) ValidateSaveRecord(
	ctx context.Context, _ *toyohrpb.ValidateSaveRecordRequest,
) (*toyohrpb.ValidateSaveRecordResponse, error) {
	if _, ok := s.caller(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	return &toyohrpb.ValidateSaveRecordResponse{}, nil
}
