package toyohr

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/switchnet/internal/db"
	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// SelectVsParams, SelectVsSchedules, SelectSeasonSchedules and
// SelectLeagueSchedules answer "there is currently no schedule", the same
// "minimum that does not crash the client" ROADMAP §2.2 already plans for
// hydro and leaderboard.
//
// TODO(capture-needed): the map/mode/weapon rotation content itself. Unlike a
// fest, which the operator authors and SaveFest validates, a normal Vs
// rotation is Nintendo's own game design data -- there is no dump or capture
// this server has of it, and CLAUDE.md is explicit that a genuinely
// undocumented shape must not be filled with an invented-but-plausible
// Nintendo internal. An empty schedule list is honest: the game reads it as
// "nothing scheduled right now" rather than crashing on a malformed one,
// which is strictly better than a fabricated rotation that is wrong in a way
// nothing here would ever catch. Change: these four functions.
//
// SelectCoopSchedules is different from the other four, and no longer
// empty: nn.npln.toyohr.v1.CoopSchedule's own decompiled proto already
// carries the real shape (Normal/BigRun/TeamContest as parallel
// sub-messages -- resources.pb.go), independently corroborated by
// nintendoapis/splatnet3-types' own SplatNet 3 GraphQL schema
// (CoopGroupingSchedule.{regularSchedules,bigRunSchedules,
// teamContestSchedules}, each CoopSchedule{startTime,endTime,setting}
// discriminated by __typename). What was missing was real stage/weapon/boss
// content and an authoring path, both of which SaveCoopShift and
// coop_ids.go now provide -- see internal/toyohr/README.md.

// SelectVsParams answers the current turf-war/ranked/league mode parameters.
func (s *Service) SelectVsParams(
	_ context.Context, _ *toyohrpb.SelectVsParamsRequest,
) (*toyohrpb.SelectVsParamsResponse, error) {
	return &toyohrpb.SelectVsParamsResponse{}, nil
}

// SelectVsSchedules answers the Vs (turf war / ranked / league) rotation.
func (s *Service) SelectVsSchedules(
	_ context.Context, _ *toyohrpb.SelectVsSchedulesRequest,
) (*toyohrpb.SelectVsSchedulesResponse, error) {
	return &toyohrpb.SelectVsSchedulesResponse{}, nil
}

// SelectCoopSchedules answers the Salmon Run rotation -- every operator-
// scheduled shift that has not fully ended yet, soonest first.
//
// Unlike currentFest, this does not pick just one: a console reasonably
// wants to know about more than the shift live right now (Splatoon 3 shows
// upcoming rotations in its Salmon Run menu before they open), so every shift
// whose End is still in the future is included.
func (s *Service) SelectCoopSchedules(
	ctx context.Context, _ *toyohrpb.SelectCoopSchedulesRequest,
) (*toyohrpb.SelectCoopSchedulesResponse, error) {
	rows, err := s.store.ListCoopShifts(ctx)
	if err != nil {
		s.log.Error("toyohr: list coop shifts", "error", err)
		return nil, status.Error(codes.Internal, "")
	}

	now := s.now()
	resp := &toyohrpb.SelectCoopSchedulesResponse{}
	for _, row := range rows {
		if !row.EndAt.After(now) {
			continue
		}
		resp.Schedules = append(resp.Schedules, s.coopScheduleProto(row))
	}
	return resp, nil
}

// SelectSeasonSchedules answers the current ranked/league season.
func (s *Service) SelectSeasonSchedules(
	_ context.Context, _ *toyohrpb.SelectSeasonSchedulesRequest,
) (*toyohrpb.SelectSeasonSchedulesResponse, error) {
	return &toyohrpb.SelectSeasonSchedulesResponse{}, nil
}

// SelectLeagueSchedules answers the current league rotation.
func (s *Service) SelectLeagueSchedules(
	_ context.Context, _ *toyohrpb.SelectLeagueSchedulesRequest,
) (*toyohrpb.SelectLeagueSchedulesResponse, error) {
	return &toyohrpb.SelectLeagueSchedulesResponse{}, nil
}

// SaveCoopShift validates a coop shift and stores it, the same split
// SaveFest makes: validation happens here (in front of the store, which is
// deliberately free of business logic) because the reasons a shift is
// invalid -- a stage that does not exist, a weapon that is not real, slot
// misalignment -- need explaining, which is not something a column can
// carry. The shift's ID is derived, not supplied: two calls describing the
// same kind and start time upsert the same row, which is what lets the CLI
// re-run a schedule command to fix a typo rather than leaving a stray entry.
func (s *Service) SaveCoopShift(ctx context.Context, c CoopShift) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return s.store.UpsertCoopShift(ctx, RowFromCoopShift(c))
}

// ListCoopShiftsForOperator returns every stored coop shift, soonest first --
// for switchnetctl and, eventually, the dashboard. Named distinctly from the
// gRPC-facing SelectCoopSchedules: that one also filters to unended shifts
// and returns the wire type, not the domain type.
func (s *Service) ListCoopShiftsForOperator(ctx context.Context) ([]CoopShift, error) {
	rows, err := s.store.ListCoopShifts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CoopShift, 0, len(rows))
	for _, row := range rows {
		out = append(out, coopShiftFromRow(row))
	}
	return out, nil
}

// DeleteCoopShift removes a coop shift by its derived ID. Not an error if it
// does not exist, matching the store's own idempotent-delete contract.
func (s *Service) DeleteCoopShift(ctx context.Context, id string) error {
	return s.store.DeleteCoopShift(ctx, id)
}

// GenerateAndSaveCoopShifts fills count Normal rotation slots starting at
// from with GenerateCoopShifts (coop_generate.go)'s own generator, storing
// every one that does not already occupy that slot.
//
// "Already occupy" is deliberately whole -- a slot with ANY existing shift is
// left alone, whether that shift was hand-scheduled or produced by an
// earlier call to this same method. That is what makes generation additive
// rather than destructive: an operator who manually corrects one slot in a
// generated run, then generates again over a wider range, never has that
// correction silently overwritten. Re-run with a fresh seed to actually
// replace a stretch of generated shifts -- delete them first.
func (s *Service) GenerateAndSaveCoopShifts(
	ctx context.Context, from time.Time, count int, duration time.Duration, rng *rand.Rand,
) (created, skipped []CoopShift, err error) {
	for _, c := range GenerateCoopShifts(rng, from, count, duration) {
		_, err := s.store.GetCoopShift(ctx, c.ID())
		switch {
		case err == nil:
			skipped = append(skipped, c)
			continue
		case errors.Is(err, db.ErrNotFound):
			// falls through to save below
		default:
			return created, skipped, err
		}
		if err := s.SaveCoopShift(ctx, c); err != nil {
			// Validate should never actually reject a generator's own
			// output -- every id it uses comes from coop_ids.go -- so this
			// is defensive, not an expected path.
			return created, skipped, err
		}
		created = append(created, c)
	}
	return created, skipped, nil
}

// RowFromCoopShift converts a shift into a storable row.
func RowFromCoopShift(c CoopShift) db.CoopShift {
	return db.CoopShift{
		ID:           c.ID(),
		Kind:         c.Kind.String(),
		StageID:      c.StageID,
		Boss:         c.Boss,
		Weapons:      append([]int64(nil), c.Weapons[:]...),
		KumaWeaponID: c.KumaWeaponID,
		RewardGearID: c.RewardGearID,
		Rewards:      append([]int64(nil), c.Rewards...),
		StartAt:      c.Start.UTC(),
		EndAt:        c.End.UTC(),
	}
}

// coopShiftFromRow converts a stored row into the domain type. Malformed
// stored data (a weapons slice of the wrong length, from a row written by a
// version of this code with a different shape) is defensive: it would only
// happen from a hand-edited database, since SaveCoopShift's Validate never
// stores anything else.
func coopShiftFromRow(r db.CoopShift) CoopShift {
	kind, _ := ParseCoopShiftKind(r.Kind)
	c := CoopShift{
		Kind:         kind,
		Start:        r.StartAt.UTC(),
		End:          r.EndAt.UTC(),
		StageID:      r.StageID,
		Boss:         r.Boss,
		KumaWeaponID: r.KumaWeaponID,
		RewardGearID: r.RewardGearID,
		Rewards:      append([]int64(nil), r.Rewards...),
	}
	copy(c.Weapons[:], r.Weapons)
	return c
}

// coopScheduleProto renders a stored shift as the wire message.
//
// Three fields are an operator-local assumption rather than a confirmed
// shape, in the same spirit as festEntryProto's resource-name guess --
// documented in docs/QUESTIONS.md rather than left unexplained:
//
//   - Name, ScheduleSetId and ShiftId follow the "tenants/<t>/..." pattern
//     GetFestDecryptionKey already confirms for a different resource, since
//     no capture shows what these three actually carry for a CoopSchedule.
//     Unlike Fest.Resource, nothing here derives a decryption IV from the
//     string, so a wrong guess costs a cosmetic mismatch, not a broken RPC.
//   - RewardType is left "" (unset) rather than guessed, since no source
//     this project has seen says what the real enum-like strings are.
func (s *Service) coopScheduleProto(r db.CoopShift) *toyohrpb.CoopSchedule {
	name := s.tenantPath() + "/coopSchedules/" + r.ID
	out := &toyohrpb.CoopSchedule{
		Name:          name,
		ScheduleSetId: r.ID,
		ShiftId:       r.ID,
		StartTime:     timestamppb.New(r.StartAt),
		EndTime:       timestamppb.New(r.EndAt),
		Timestamp:     timestamppb.New(s.now()),
	}
	switch r.Kind {
	case "big_run":
		out.BigRun = &toyohrpb.CoopSchedule_BigRun{
			Stage:        r.StageID,
			Boss:         r.Boss,
			MainWeapons:  append([]int64(nil), r.Weapons...),
			KumaWeapon:   int32(r.KumaWeaponID),
			RewardGearId: r.RewardGearID,
			Rewards:      append([]int64(nil), r.Rewards...),
		}
	default:
		out.Normal = &toyohrpb.CoopSchedule_Normal{
			Stage:        r.StageID,
			Boss:         r.Boss,
			MainWeapons:  append([]int64(nil), r.Weapons...),
			KumaWeapon:   int32(r.KumaWeaponID),
			RewardGearId: r.RewardGearID,
		}
	}
	return out
}
