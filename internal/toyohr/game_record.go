package toyohr

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"google.golang.org/protobuf/encoding/prototext"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
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
	// The console names itself with this alias; identity still comes from the token.
	if u := req.GetUser(); u != "tenants/current/users/current" && u != "" && lastSegment(u) != uid {
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
			// coop_shift is the game opening a rotation; the reset has to
			// happen before Put records the new shift_id, which is what
			// tells a repeat of the same shift apart from a new one.
			// A failed reset must not fail the RPC: an error here reaches the
			// lobby as an error code. Leaving shift_id unrecorded makes the
			// game's next coop_shift try the reset again.
			shift, ok, err := coopShiftByID(ctx, s.store, id)
			if err == nil && ok {
				var reset bool
				reset, err = RollCoopRotation(ctx, s.store, s.tenant, uid, shift, s.now().UTC())
				if reset {
					s.log.Info("toyohr: Salmon Run points reset for a new rotation", "user", uid, "shift", id)
				}
			}
			if err != nil {
				s.log.Error("toyohr: Salmon Run rotation reset", "user", uid, "error", err)
				delete(attrs.Fields, "shift_id")
			}
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
		// initial_vs carries the starting matchmaking rating the console
		// chose. The working capture's VsUserAttribute keeps one rating per
		// mode (tw, ar, lf, gl, cl) as mmr_rate_<mode>, flagged by
		// mmr_initialized_<mode>. Without the flags the console treats the
		// rating as uninitialised and sends initial_vs again: on hardware on
		// 2026-09-25 it did so ~10 times a second, forever, from the hall.
		// Only the rate the console sent is stored; rd/vol keep the game's
		// own defaults.
		if iv := req.GetInitialVs(); iv != nil && iv.GetInitialRate() != 0 {
			for _, mode := range []string{"tw", "ar", "lf", "gl", "cl"} {
				attrs.Fields["mmr_rate_"+mode] = &commonpb.Value{ValueType: &commonpb.Value_DoubleValue{DoubleValue: iv.GetInitialRate()}}
				attrs.Fields["mmr_initialized_"+mode] = &commonpb.Value{ValueType: &commonpb.Value_BooleanValue{BooleanValue: true}}
			}
		}
	}

	name := "tenants/current/documents/services/GameRecord/users/" + uid + "/" + record + "/Data"
	doc, err := s.docs.Put(ctx, uid, name, attrs)
	if err != nil {
		return nil, err
	}
	s.log.Info("toyohr: initialised GameRecord attributes",
		"user", uid, "record", record, "type", req.GetInitializeType(), "keys", len(doc.GetFields().GetFields()),
		"request", prototext.MarshalOptions{}.Format(req))
	return &toyohrpb.InitializeAttributesResponse{
		Attributes: doc.GetFields(),
		Document:   doc.GetName(),
	}, nil
}

// InitializeTag, GetTag and SelectTags answer instead of the embedded
// Unimplemented default, for the same reason WriteSaveRecord does
// (internal/toyohr/cloud_save.go): all three response messages are marked
// "[UNKNOWN]" in kinnay/NPLN-Protocols' own reconstruction
// (.proto-upstream/latest/proto/toyohr/v1/game_record.proto) and generate
// with zero fields, so acknowledging them invents nothing -- there is
// nothing to invent. Unimplemented is a harsher gRPC error than a
// structurally valid empty response; a caller that reaches these RPCs at
// all (which requires seeing real League schedule data first -- see
// docs/CAPTURES/splat3-hall-boundary-2026-09-24.md) has already been told
// League exists, so refusing outright here is the more likely crash, not
// the safer default.
//
// No Tag is actually stored: there is nowhere honest to keep one yet
// (SelectTags' own response has no field to list one in either), so
// InitializeTag's acknowledgement does not persist anything -- unlike
// InitializeAttributes, which does. If a capture ever settles these
// messages' real fields, that is the point to add storage.
//
// TODO(capture-needed): the real Tag/InitializeTagResponse/
// GetTagResponse/SelectTagsResponse shapes -- neither preserved working
// capture ever entered League Battle, so these RPCs were never observed on
// the wire.
func (s gameRecordService) InitializeTag(
	ctx context.Context, req *toyohrpb.InitializeTagRequest,
) (*toyohrpb.InitializeTagResponse, error) {
	if _, ok := s.caller(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	s.log.Info("toyohr: InitializeTag acknowledged",
		"users", len(req.GetUsers()), "league_schedule", req.GetLeagueSchedule(), "game_rule", req.GetGameRule())
	return &toyohrpb.InitializeTagResponse{}, nil
}

func (s gameRecordService) GetTag(
	ctx context.Context, req *toyohrpb.GetTagRequest,
) (*toyohrpb.GetTagResponse, error) {
	if _, ok := s.caller(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	s.log.Info("toyohr: GetTag acknowledged", "name", req.GetName())
	return &toyohrpb.GetTagResponse{}, nil
}

func (s gameRecordService) SelectTags(
	ctx context.Context, req *toyohrpb.SelectTagsRequest,
) (*toyohrpb.SelectTagsResponse, error) {
	if _, ok := s.caller(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	s.log.Info("toyohr: SelectTags acknowledged", "user", req.GetUser())
	return &toyohrpb.SelectTagsResponse{}, nil
}

func stringValue(v string) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: v}}
}

func intValue(v int64) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: v}}
}

// CreateFestPowerMeasurement records a player's first Splatfest power
// measurement, and stores the document the game reads it back from.
//
// Its shape is a real Nintendo capture that NextendoNetwork/splatoon-3
// records (a new account's exchange; facts only, none of its code used):
// named tenants/<t>/users/<uid>/festPowerMeasurements/<festId>, fields
// fest_id, npln_user_id, fest_power_challenge, is_initial=true, created_at,
// battle_num=0, and the document the game then reads,
// .../documents/services/GameRecord/users/<uid>/FestPowerMeasurement/<festId>.
//
// Both failure modes are measured there. Answered with nothing, the game
// never learns which document holds its measurement, re-reads it, finds
// nothing and creates it again: 1,058 creations in two minutes, the player
// stuck at "Connecting to the Internet". Answered with a document name it
// cannot parse, the game crashes.
//
// Like InitializeAttributes, the fields carry only what is known: the
// request's own, the fest id and user, and the two constants the capture
// shows. fest_power_challenge is kept only if the request sent one, and
// created_at is left out -- its value type is not recorded -- so the game
// keeps its own default for both.
//
// The document keeps this package's tenants/current naming (see
// InitializeAttributes): a switch to the concrete tenant was tried on
// hardware and reverted.
//
// TODO(capture-needed): whether a real server fills fest_power_challenge
// and created_at when the request leaves them out. Change: this function.
func (s gameRecordService) CreateFestPowerMeasurement(
	ctx context.Context, req *toyohrpb.CreateFestPowerMeasurementRequest,
) (*toyohrpb.FestPowerMeasurement, error) {
	if s.docs == nil {
		return nil, status.Error(codes.Unimplemented, "not implemented")
	}
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	sent := req.GetFestPowerMeasurement().GetFields().GetFields()
	festID := sent["fest_id"].GetStringValue()
	// The id goes straight into a document name, and a name the game cannot
	// parse crashes it (2162-0001), so anything not shaped like a fest id is
	// refused.
	if !festIDPattern.MatchString(festID) {
		s.log.Warn("toyohr: fest power measurement with a malformed fest id",
			"user", uid, "fest_id", festID)
		return nil, status.Error(codes.InvalidArgument, "malformed fest id")
	}

	fields := &commonpb.MapValue{Fields: map[string]*commonpb.Value{}}
	for k, v := range sent {
		fields.Fields[k] = v
	}
	fields.Fields["fest_id"] = stringValue(festID)
	fields.Fields["npln_user_id"] = stringValue(uid)
	fields.Fields["is_initial"] = &commonpb.Value{ValueType: &commonpb.Value_BooleanValue{BooleanValue: true}}
	fields.Fields["battle_num"] = intValue(0)

	name := "tenants/current/documents/services/GameRecord/users/" + uid + "/FestPowerMeasurement/" + festID
	doc, err := s.docs.Put(ctx, uid, name, fields)
	if err != nil {
		return nil, err
	}
	s.log.Info("toyohr: recorded a fest power measurement", "user", uid, "fest", festID)
	return &toyohrpb.FestPowerMeasurement{
		Name:       "tenants/" + s.tenant + "/users/" + uid + "/festPowerMeasurements/" + festID,
		Fields:     doc.GetFields(),
		CreateTime: doc.GetCreateTime(),
		UpdateTime: doc.GetUpdateTime(),
		Document:   doc.GetName(),
	}, nil
}

// CreateBankaraChallenge starts an Anarchy Battle (Series) challenge.
//
// Shaped like CreateFestPowerMeasurement, the GameRecord call it sits beside:
// the request carries the challenge's fields, and the answer is the stored
// document under the player's GameRecord, named BankaraChallenges/<id> --
// the collection NextendoNetwork/splatoon-3's notes show the game reading back
// (and aborting with 2162-0001 when it came back with no name).
//
// TODO(capture-needed): the id Nintendo assigns and any fields it adds. The
// fields here are the game's own, plus npln_user_id. Change: this function.
func (s gameRecordService) CreateBankaraChallenge(
	ctx context.Context, req *toyohrpb.CreateBankaraChallengeRequest,
) (*toyohrpb.BankaraChallenge, error) {
	if s.docs == nil {
		return nil, status.Error(codes.Unimplemented, "not implemented")
	}
	uid, ok := s.caller(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthenticated")
	}
	fields := &commonpb.MapValue{Fields: map[string]*commonpb.Value{}}
	for k, v := range req.GetBankaraChallenge().GetFields().GetFields() {
		fields.Fields[k] = v
	}
	fields.Fields["npln_user_id"] = stringValue(uid)

	id := strings.ReplaceAll(uuid.NewString(), "-", "")
	name := "tenants/current/documents/services/GameRecord/users/" + uid + "/BankaraChallenges/" + id
	doc, err := s.docs.Put(ctx, uid, name, fields)
	if err != nil {
		return nil, err
	}
	s.log.Info("toyohr: started an Anarchy Series challenge", "user", uid, "id", id)
	return &toyohrpb.BankaraChallenge{
		Name:       "tenants/" + s.tenant + "/users/" + uid + "/bankaraChallenges/" + id,
		Fields:     doc.GetFields(),
		CreateTime: doc.GetCreateTime(),
		UpdateTime: doc.GetUpdateTime(),
		Document:   doc.GetName(),
	}, nil
}
