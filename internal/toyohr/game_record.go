package toyohr

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// InitializeAttributes creates a player's GameRecord document and answers with
// it: {attributes, document}.
//
// The console calls this when a GetDocument for its Versus or Salmon Run
// record finds nothing, then reads back the document this names -- so the
// document has to exist in Ugcstore afterwards with the same fields, or it
// re-initialises forever. The request says which record: field 15
// (initial_coop) or 16 (coop_shift) is Salmon Run, anything else Versus; a
// Salmon Run record handed back for a Versus request is what crashed the game
// in NextendoNetwork/splatoon-3's measurement.
//
// The attributes carry only what the request itself says -- no invented rank,
// rating or X Power. The game's own parser reads every key only if it is
// present and keeps its built-in default otherwise, so an absent key is a
// fresh player's value, not a gap. See
// docs/CAPTURES/npln-gamerecord-attributes-2026-09-23.md.
//
// TODO(capture-needed): whether the console accepts a record this sparse on
// hardware, and which of shift_id/limited_shift_id a coop_shift sets (job
// types other than the regular rotation may use the latter).
func (s gameRecordService) InitializeAttributes(
	ctx context.Context, req *toyohrpb.InitializeAttributesRequest,
) (*toyohrpb.InitializeAttributesResponse, error) {
	if s.docs == nil {
		return nil, status.Error(codes.Unimplemented, "not implemented")
	}
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	if u := req.GetUser(); u != "" && lastSegment(u) != uid {
		s.log.Warn("toyohr: InitializeAttributes for another player refused",
			"caller", uid, "user", u)
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}

	attrs := &commonpb.MapValue{Fields: map[string]*commonpb.Value{}}
	record := "VsUserAttribute"
	switch {
	case req.GetInitialCoop() != nil || req.GetCoopShift() != nil || req.GetCoopGrade() != nil:
		record = "CoopUserAttribute"
		if id := req.GetCoopShift().GetShiftId(); id != "" {
			attrs.Fields["shift_id"] = stringValue(id)
		}
		if g := req.GetCoopGrade(); g != nil {
			attrs.Fields["total_grade_point"] = intValue(int64(g.GetTotalGradePoint()))
		}
	default:
		season := req.GetInitialVs().GetSeasonId()
		if ns := req.GetNewSeason().GetSeasonId(); ns != 0 {
			season = ns
		}
		if season != 0 {
			attrs.Fields["season_id"] = intValue(int64(season))
		}
	}

	name := "tenants/current/documents/services/GameRecord/users/" + uid + "/" + record + "/Data"
	doc, err := s.docs.Put(ctx, uid, name, attrs)
	if err != nil {
		return nil, err
	}
	s.log.Info("toyohr: initialised GameRecord attributes",
		"user", uid, "record", record, "type", req.GetInitializeType(), "keys", len(doc.GetFields().GetFields()))
	return &toyohrpb.InitializeAttributesResponse{
		Attributes: doc.GetFields(),
		Document:   doc.GetName(),
	}, nil
}

func stringValue(v string) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: v}}
}

func intValue(v int64) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: v}}
}
