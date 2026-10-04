package toyohr

import (
	"context"
	"strconv"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// The five GameRecord calls below had no known shape anywhere. Their wire
// shapes -- field numbers and types, requests and responses -- were recovered
// from the game's own generated parsers (Splatoon 3 28C4287A; see
// docs/CAPTURES/README.md, "GameRecord calls recovered from the game's
// parsers"). What a real server puts in each answer is not known: none has
// ever been called on grid0, so every handler logs its full request.

// PrepareWeaponPowerMeasurement answers with the player's
// WeaponPowerMeasurement document for one weapon, creating it empty if
// absent. The game names those documents
// services/GameRecord/users/%s/WeaponPowerMeasurement/%d and already reads
// them through ugcstore, answered named and empty.
//
// TODO(capture-needed): the fields a real server fills in, and whether field
// 2 of the request really is the weapon id. Change: this function.
func (s gameRecordService) PrepareWeaponPowerMeasurement(
	ctx context.Context, req *toyohrpb.PrepareWeaponPowerMeasurementRequest,
) (*toyohrpb.PrepareWeaponPowerMeasurementResponse, error) {
	m, err := s.preparePowerMeasurement(ctx, req, "WeaponPowerMeasurement", req.GetWeapon())
	if err != nil {
		return nil, err
	}
	return &toyohrpb.PrepareWeaponPowerMeasurementResponse{WeaponPowerMeasurement: m}, nil
}

// PrepareBankaraPowerMeasurement answers like PrepareWeaponPowerMeasurement.
// The game carries no document path for this kind; the name used here
// mirrors the Weapon and Fest ones, keyed by the request's integer field.
//
// TODO(capture-needed): the document name, what request fields 2 and 3 are,
// and the fields a real server fills in. Change: this function.
func (s gameRecordService) PrepareBankaraPowerMeasurement(
	ctx context.Context, req *toyohrpb.PrepareBankaraPowerMeasurementRequest,
) (*toyohrpb.PrepareBankaraPowerMeasurementResponse, error) {
	m, err := s.preparePowerMeasurement(ctx, req, "BankaraPowerMeasurement", req.GetUnknown_3())
	if err != nil {
		return nil, err
	}
	return &toyohrpb.PrepareBankaraPowerMeasurementResponse{BankaraPowerMeasurement: m}, nil
}

// PrepareLeaguePowerMeasurement answers like PrepareBankaraPowerMeasurement.
//
// TODO(capture-needed): as PrepareBankaraPowerMeasurement, plus what the
// oneof between request fields 1 and 4 selects. Change: this function.
func (s gameRecordService) PrepareLeaguePowerMeasurement(
	ctx context.Context, req *toyohrpb.PrepareLeaguePowerMeasurementRequest,
) (*toyohrpb.PrepareLeaguePowerMeasurementResponse, error) {
	m, err := s.preparePowerMeasurement(ctx, req, "LeaguePowerMeasurement", req.GetUnknown_3())
	if err != nil {
		return nil, err
	}
	return &toyohrpb.PrepareLeaguePowerMeasurementResponse{LeaguePowerMeasurement: m}, nil
}

// preparePowerMeasurement gets or creates the caller's own measurement
// document of one kind. Put merges, so an existing measurement keeps its
// fields; the caller is always the owner, never a user named in the request.
func (s gameRecordService) preparePowerMeasurement(
	ctx context.Context, req proto.Message, kind string, id int64,
) (*toyohrpb.PowerMeasurement, error) {
	if s.docs == nil {
		return nil, status.Error(codes.Unimplemented, "not implemented")
	}
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	s.logPrepare(uid, kind, req)
	if id < 0 {
		return nil, status.Error(codes.InvalidArgument, "negative measurement id")
	}
	name := "tenants/current/documents/services/GameRecord/users/" + uid + "/" + kind + "/" + strconv.FormatInt(id, 10)
	doc, err := s.docs.Put(ctx, uid, name, &commonpb.MapValue{})
	if err != nil {
		return nil, err
	}
	return &toyohrpb.PowerMeasurement{
		Name:       doc.GetName(),
		Fields:     doc.GetFields(),
		CreateTime: doc.GetCreateTime(),
		UpdateTime: doc.GetUpdateTime(),
		Document:   doc.GetName(),
	}, nil
}

// PrepareTag answers with an empty tag.
//
// TODO(capture-needed): what the three fields of the tag (string, repeated
// string, string) hold. Change: this function.
func (s gameRecordService) PrepareTag(
	ctx context.Context, req *toyohrpb.PrepareTagRequest,
) (*toyohrpb.PrepareTagResponse, error) {
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	s.logPrepare(uid, "Tag", req)
	return &toyohrpb.PrepareTagResponse{Tag: &toyohrpb.PreparedTag{}}, nil
}

// ResetUdemae answers with an empty response and changes nothing.
//
// TODO(capture-needed): what a real server resets, and what the response's
// integer, map and string carry. Change: this function.
func (s gameRecordService) ResetUdemae(
	ctx context.Context, req *toyohrpb.ResetUdemaeRequest,
) (*toyohrpb.ResetUdemaeResponse, error) {
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	s.logPrepare(uid, "Udemae", req)
	return &toyohrpb.ResetUdemaeResponse{}, nil
}

func (s gameRecordService) logPrepare(uid, kind string, req proto.Message) {
	body, _ := protojson.Marshal(req)
	s.log.Info("toyohr: GameRecord call with a recovered shape", "user", uid, "kind", kind, "request", string(body))
}
