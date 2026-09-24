package toyohr

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/switchnet/proto/common"
	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// GetSaveRecord answers with a real, resolved SaveRecord for every account,
// never NotFound and never a genuinely empty message.
//
// NextendoNetwork/splatoon-3 measured (facts only, not its code) three
// behaviours on real hardware for a player with no save record: NotFound
// makes the game create nothing and loop on a communication error; a
// genuinely empty success makes it stop dead -- no CreateSaveRecord, no
// FestSchedule, no documents; only a real, structurally-complete record made
// everything resume. This is the same crash family as noFestSentinel in
// service.go, on the RPC that fires immediately before the console's first
// gRPC calls go silent on SwitchNet's own hardware on 2026-09-24.
//
// SaveData is deliberately EMPTY, not seeded: CLAUDE.md is explicit that a
// game dump or save data is something to ask the operator about rather than
// invent, and Nextendo's own captured content is a stranger's real save
// progression, out of scope here regardless. This is the shape a save
// record needs, honestly blank -- not reconstructed Nintendo or Nextendo
// data.
//
// Create/Validate/ChangeUserName/Delete are left to
// UnimplementedCloudSaveServer: nothing calls them this early, and
// implementing "create" or "delete" convincingly would mean this server
// keeping opaque save blobs it has no way to validate -- a different scope
// than "answer the boot queries that would otherwise hang". WriteSaveRecord
// is answered, not left Unimplemented -- see its own doc comment below for
// why this one is different.
func (s *Service) GetSaveRecord(
	ctx context.Context, _ *toyohrpb.GetSaveRecordRequest,
) (*toyohrpb.SaveRecord, error) {
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "")
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
