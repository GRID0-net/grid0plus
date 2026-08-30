package toyohr

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// GetSaveRecord answers "no cloud save exists yet" for every account.
//
// SwitchNet has no cloud save infrastructure -- CLAUDE.md is explicit that a
// game dump or save data is something to ask the operator about rather than
// invent, and a save record is exactly that: real player save state this
// server has no legitimate way to have. NotFound is not a placeholder for
// missing functionality here, it is a correct answer: a fresh account has no
// cloud save on a real server either, and the game already has to handle
// that case (it is a new-account console's very first boot).
//
// Every other CloudSave RPC (Create/Validate/Write/ChangeUserName/Delete) is
// left to UnimplementedCloudSaveServer, unlike this one: nothing calls them
// this early, and implementing "write" convincingly would mean this server
// keeping opaque save blobs it has no way to validate -- a different scope
// than "answer the boot query that would otherwise hang".
func (s *Service) GetSaveRecord(
	_ context.Context, _ *toyohrpb.GetSaveRecordRequest,
) (*toyohrpb.SaveRecord, error) {
	return nil, status.Error(codes.NotFound, "no cloud save record")
}
