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
// Every other CloudSave RPC (Create/Validate/Write/ChangeUserName/Delete) is
// left to UnimplementedCloudSaveServer: nothing calls them this early, and
// implementing "write" convincingly would mean this server keeping opaque
// save blobs it has no way to validate -- a different scope than "answer the
// boot query that would otherwise hang".
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
