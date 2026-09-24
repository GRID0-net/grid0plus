package toyohr

import (
	"context"

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
// SwitchNet never actually holds a player's cloud save (CreateSaveRecord is
// intentionally left Unimplemented -- see its own comment), so every caller
// but the private diagnostic probe gets the true zero-message answer, not a
// constructed one: this is not a placeholder standing in for real save
// content, it is what "this server has nothing for you" honestly looks
// like on the wire.
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
	now := timestamppb.New(s.now())
	return &toyohrpb.SaveRecord{
		Name:       s.tenantPath() + "/saveRecords/" + uid,
		SaveData:   &common.MapValue{Fields: map[string]*common.Value{}},
		CreateTime: now,
		UpdateTime: now,
	}, nil
}

// WriteSaveRecord acknowledges a write without storing it.
//
// Answered rather than left Unimplemented, unlike the other four -- because
// unlike them, this one is not merely uncalled early, it is called
// immediately as a direct consequence of GetSaveRecord's own honestly-blank
// answer. NextendoNetwork/splatoon-3's own proto comment (facts only, not
// its code) documents the real shape: the client sends an
// evt=Validate-style WriteSaveRecord carrying exactly the keys missing from
// a blank record (CloudRandomSet, HaveGearClothesMap, HaveGearHeadMap,
// HaveGearShoesMap) as soon as it sees one. Confirmed on SwitchNet's own
// hardware on 2026-09-24: a VM-side loopback capture showed the console
// calling this RPC twice, immediately after GetSaveRecord and
// SelectVsParams both succeeded, getting Unimplemented both times, and
// going silent for good shortly after.
//
// WriteSaveRecordResponse carries exactly one field -- a timestamp, per
// both this project's own proto and NextendoNetwork/splatoon-3's comment
// (`WriteSaveRecordResponse{timestamp}`) -- so acknowledging costs nothing
// this server does not already have. Nothing is stored: SwitchNet still has
// no legitimate way to keep real player save content, the same boundary
// GetSaveRecord's own doc comment draws. This only ever tells the client
// "your write was accepted," on a save this server already answers blank
// every time regardless.
func (s *Service) WriteSaveRecord(
	ctx context.Context, _ *toyohrpb.WriteSaveRecordRequest,
) (*toyohrpb.WriteSaveRecordResponse, error) {
	if _, ok := s.caller(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	return &toyohrpb.WriteSaveRecordResponse{
		Timestamp: timestamppb.New(s.now()),
	}, nil
}
