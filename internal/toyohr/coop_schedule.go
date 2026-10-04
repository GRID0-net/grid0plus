package toyohr

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/proto/common"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// CoopShiftKind is which of Salmon Run's three modes a shift belongs to.
type CoopShiftKind int

const (
	// CoopShiftNormal is the ordinary Salmon Run rotation.
	CoopShiftNormal CoopShiftKind = iota
	// CoopShiftBigRun is a Big Run event.
	CoopShiftBigRun
	// CoopShiftTeamContest is a Team Contest (Eggstra Work) event.
	CoopShiftTeamContest
)

func (k CoopShiftKind) String() string {
	switch k {
	case CoopShiftBigRun:
		return "big_run"
	case CoopShiftTeamContest:
		return "team_contest"
	default:
		return "normal"
	}
}

// ParseCoopShiftKind parses "normal", "big_run" or "team_contest", the same
// strings CoopShiftKind.String produces -- what the CLI and the store both
// use, so there is exactly one place the three spellings are decided.
func ParseCoopShiftKind(s string) (CoopShiftKind, error) {
	switch s {
	case "normal":
		return CoopShiftNormal, nil
	case "big_run":
		return CoopShiftBigRun, nil
	case "team_contest":
		return CoopShiftTeamContest, nil
	default:
		return 0, fmt.Errorf("coop shift kind %q must be \"normal\", \"big_run\" or \"team_contest\"", s)
	}
}

// CoopShift is one Salmon Run rotation slot, as the operator schedules it.
//
// Unlike Fest, there is no separate content pack: everything the console
// needs is real Nintendo asset ids (coop_ids.go) carried directly in the
// CoopSchedule message, so scheduling one is a one-part problem, not two.
type CoopShift struct {
	Kind CoopShiftKind

	// Start and End must land on Splatoon 3's two-hour slot boundary --
	// fest.go's AlignToSlot/IsSlotAligned, which its own comment already
	// states as a fact about how the game files every schedule, not
	// something specific to fests.
	Start, End time.Time

	// StageID must be a real CoopStages entry whose BigRun flag matches Kind.
	StageID int32

	// Boss is the king salmonid's codename for this shift's Xtrawave
	// (coop_ids.go's CoopBosses), or "" if none is scheduled.
	Boss string

	// Weapons are exactly four real CoopWeapons ids, distinct -- every real
	// rotation this project has seen shows exactly four.
	Weapons [4]int64

	// KumaWeaponID is the Grizzco weapon (CoopKumaWeapons) this shift's boss
	// wave hands out.
	KumaWeaponID int64

	// RewardGearID is a gear reward for this shift, or 0 for none. Not
	// validated against a real table -- coop_ids.go has none; see its
	// comment for why.
	RewardGearID int32

	// Rewards are a Big Run or Team Contest's scale-tier reward values
	// (CoopSchedule_BigRun.Rewards / CoopSchedule_TeamContest.Rewards). Empty
	// for CoopShiftNormal, which has no such field on the wire. Their exact
	// meaning is unconfirmed -- see docs/QUESTIONS.md -- so nothing here
	// validates their contents, only that a Normal shift has none.
	Rewards []int64

	// RewardBorders are a Big Run's gold, silver and bronze high-score
	// borders: golden eggs a team delivers in one job. The game's
	// CoopSchedule parser (main+0x32a28d0) reads them from
	// attributes.BigRunParams.RewardBorders.{Gold,Silver,Bronze} and
	// invalidates the shift unless all three are positive, so a Big Run
	// needs them; at the end of the event the console compares its
	// PointCardBigRun best_golden_ikura_num with them and writes its own
	// trophy. Zero for every other kind. The real events' numbers are not
	// known here: the operator sets them.
	RewardBorders [3]int64
	// IsSpecial is BigRunParams.IsSpecial; the game defaults it to false.
	IsSpecial bool
}

// ID derives a stable identifier from the shift's kind and start time, the
// same "derived, never typed twice" reasoning as fest.go's NewFestID: two
// shifts of the same kind starting at once would otherwise need the operator
// to invent a name that has to stay unique by hand.
func (c CoopShift) ID() string {
	return fmt.Sprintf("%s-%d", c.Kind, c.Start.UTC().Unix())
}

// Validate reports why a shift cannot be served, or nil.
//
// Every check here is one that would otherwise reach the console as a
// message it either cannot resolve against its own asset tables (an id
// coop_ids.go does not carry) or that contradicts the schedule Splatoon 3
// expects (fest.go's slot-boundary reasoning, restated for Coop by that
// file's own comment).
func (c CoopShift) Validate() error {
	if c.Start.IsZero() {
		return fmt.Errorf("coop shift has no start time")
	}
	if !IsSlotAligned(c.Start) {
		return fmt.Errorf("coop shift starts at %s, which is not on a two-hour slot boundary; "+
			"Splatoon 3 files every schedule into two-hour slots",
			c.Start.UTC().Format(time.RFC3339))
	}
	if c.End.IsZero() || !c.End.After(c.Start) {
		return fmt.Errorf("coop shift ends at %s, which is not after its start %s",
			c.End.UTC().Format(time.RFC3339), c.Start.UTC().Format(time.RFC3339))
	}
	if !IsSlotAligned(c.End) {
		return fmt.Errorf("coop shift ends at %s, which is not on a two-hour slot boundary",
			c.End.UTC().Format(time.RFC3339))
	}

	stage, ok := CoopStageByID(c.StageID)
	if !ok {
		return fmt.Errorf("coop shift stage id %d is not a real Salmon Run stage; "+
			"see toyohr.CoopStages", c.StageID)
	}
	switch c.Kind {
	case CoopShiftBigRun:
		if !stage.BigRun {
			return fmt.Errorf("coop shift is big_run but stage %d (%s) is not a Big Run stage",
				c.StageID, stage.Name)
		}
		for i, b := range c.RewardBorders {
			if b <= 0 {
				return fmt.Errorf("coop shift is big_run but its %s reward border is %d; "+
					"the game drops a Big Run shift unless all three are positive",
					[]string{"gold", "silver", "bronze"}[i], b)
			}
		}
	case CoopShiftNormal:
		if stage.BigRun {
			return fmt.Errorf("coop shift is normal but stage %d (%s) is a Big Run stage",
				c.StageID, stage.Name)
		}
		if len(c.Rewards) != 0 {
			return fmt.Errorf("coop shift is normal but carries %d Big Run reward tier(s); "+
				"Rewards only applies to a big_run or team_contest shift", len(c.Rewards))
		}
	case CoopShiftTeamContest:
		// Team Contest runs on the normal rotation's stages, not Big Run's --
		// the same restriction as CoopShiftNormal, and CoopSchedule_TeamContest
		// has no unk1-style Big-Run-only field the way CoopSchedule_BigRun does.
		if stage.BigRun {
			return fmt.Errorf("coop shift is team_contest but stage %d (%s) is a Big Run stage",
				c.StageID, stage.Name)
		}
		// CoopSchedule_TeamContest carries neither field: the console has
		// nowhere on the wire to put a boss or a reward gear id for this
		// kind, so a non-zero value here is an operator mistake to explain
		// now rather than a value coopScheduleProto would silently drop.
		if c.Boss != "" {
			return fmt.Errorf("coop shift is team_contest but has a boss (%q); "+
				"CoopSchedule_TeamContest has no boss field", c.Boss)
		}
		if c.RewardGearID != 0 {
			return fmt.Errorf("coop shift is team_contest but has a reward gear id (%d); "+
				"CoopSchedule_TeamContest has no reward_gear_id field", c.RewardGearID)
		}
	default:
		return fmt.Errorf("coop shift kind %d is none of CoopShiftNormal, CoopShiftBigRun "+
			"or CoopShiftTeamContest", c.Kind)
	}

	if c.Kind != CoopShiftBigRun && (c.RewardBorders != [3]int64{} || c.IsSpecial) {
		return fmt.Errorf("coop shift is %s but has Big Run reward borders or IsSpecial; "+
			"only a big_run shift carries BigRunParams", c.Kind)
	}

	if !CoopBossValid(c.Boss) {
		return fmt.Errorf("coop shift boss %q is not a real king salmonid codename; "+
			"see toyohr.CoopBosses", c.Boss)
	}

	seen := make(map[int64]bool, len(c.Weapons))
	for i, id := range c.Weapons {
		if coopRandomSlot(id) {
			continue
		}
		_, normal := CoopWeaponByID(id)
		_, grizzco := CoopKumaWeaponByID(id)
		if !normal && !grizzco {
			// A main weapon slot being a Grizzco weapon is GRID0+'s own
			// generation policy (grizzcoWeaponChance, coop_generate.go), not
			// a claim about Nintendo's own rotation -- so it must validate
			// as real, exactly like an ordinary CoopWeapons id.
			return fmt.Errorf("coop shift weapon %d (slot %d) is not a real Salmon Run weapon; "+
				"see toyohr.CoopWeapons and toyohr.CoopKumaWeapons", id, i)
		}
		if seen[id] {
			return fmt.Errorf("coop shift weapon %d appears more than once; a real rotation "+
				"never repeats a weapon across its four slots", id)
		}
		seen[id] = true
	}

	if _, ok := CoopKumaWeaponByID(c.KumaWeaponID); !ok {
		return fmt.Errorf("coop shift kuma weapon id %d is not a real Grizzco weapon; "+
			"see toyohr.CoopKumaWeapons", c.KumaWeaponID)
	}

	return nil
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
	row := db.CoopShift{
		ID:           c.ID(),
		Kind:         c.Kind.String(),
		StageID:      c.StageID,
		Boss:         c.Boss,
		Weapons:      append([]int64(nil), c.Weapons[:]...),
		KumaWeaponID: c.KumaWeaponID,
		RewardGearID: c.RewardGearID,
		Rewards:      append([]int64(nil), c.Rewards...),
		IsSpecial:    c.IsSpecial,
		StartAt:      c.Start.UTC(),
		EndAt:        c.End.UTC(),
	}
	if c.RewardBorders != [3]int64{} {
		row.RewardBorders = c.RewardBorders[:]
	}
	return row
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
		IsSpecial:    r.IsSpecial,
	}
	copy(c.Weapons[:], r.Weapons)
	copy(c.RewardBorders[:], r.RewardBorders)
	return c
}

// coopScheduleProto renders a stored shift as the wire message.
//
// A shift with no operator-selected reward uses one real gear pair from
// CoopRewardGears, picked at random but stable per slot. Every Normal entry
// in both working captures includes reward_type (Head, Clothes, or Shoes)
// and a nonzero reward_gear_id. This is GRID0+'s own reward rotation,
// not a reconstruction of Nintendo's selection policy.
//
// Name is NOT just a cosmetic mismatch if it is wrong, unlike the claim this
// comment used to make. A 2026-09-24 decompile of Splatoon 3 v11.3.0 (real
// hardware, this project's own console) found the client's resource-name
// parser for a CoopSchedule (main.nso, the function this response's Name
// reaches) requires the literal shape
// "tenants/<t>/targets/<target>/coopSchedules/<rest>" -- checked with a
// hard memcmp against "targets" and "coopSchedules" as the third and fifth
// path segments -- and hard-aborts (User Break, nn.npln.Worker) when a name
// does not parse that way. The previous shape here,
// "tenants/<t>/coopSchedules/<id>", omits the "targets/<target>" segment
// entirely and reproduced this crash on real hardware the same day, with
// four real coop shifts seeded. Both 2026-09-23 successful Nextendo
// captures use the last segment of the request Target ("default") in this
// position. The old shift.Kind value passed the path parser but named a
// different target than the one the console requested.
func (s *Service) coopScheduleProto(r db.CoopShift, target string) *toyohrpb.CoopSchedule {
	name := s.tenantPath() + "/targets/" + target + "/coopSchedules/" + r.ID
	// Both working hall captures use one schedule-set id for the whole
	// current Coop window and leave ShiftId absent. The resource Name keeps
	// each shift distinct without inventing another wire identifier.
	// Field 2 is the shift's kind, and the only thing the game decides it
	// by: the schedule parser (main+0x32a28d0) maps it through
	// main+0x32b90e0, 1 to a regular shift, 2 to a Big Run and 3 to Eggstra
	// Work, and reads the Normal, BigRun or TeamContest sub-message that kind
	// names. Both working hall captures only had regular shifts, hence 1.
	out := &toyohrpb.CoopSchedule{
		Name:          name,
		Unk:           coopScheduleKind(r.Kind),
		ScheduleSetId: rotationSetID("coop", AlignToSlot(s.now())),
		StartTime:     timestamppb.New(r.StartAt),
		EndTime:       timestamppb.New(r.EndAt),
		// Both working hall captures keep this field fixed at exactly seven
		// days before the shift's start, across repeated schedule reads. Using
		// the current request time made an otherwise unchanged rotation look
		// different on every poll while its ETag stayed the same.
		Timestamp: timestamppb.New(r.StartAt.Add(-7 * 24 * time.Hour)),
	}
	attrs := coopKumaWeaponsAttribute(r.KumaWeaponID, r.Weapons)
	reward := coopRewardForShift(r.RewardGearID, r.StartAt)
	switch r.Kind {
	case "big_run":
		attrs.Fields["BigRunParams"] = bigRunParams(r)
		out.BigRun = &toyohrpb.CoopSchedule_BigRun{
			Stage:        r.StageID,
			Boss:         r.Boss,
			MainWeapons:  append([]int64(nil), r.Weapons...),
			RewardGearId: r.RewardGearID,
			Rewards:      append([]int64(nil), r.Rewards...),
			Attributes:   attrs,
		}
	case "team_contest":
		// No Boss or RewardGearId: CoopSchedule_TeamContest carries neither
		// field, and Validate refuses to store a shift with either set, so
		// there is nothing to drop here.
		out.TeamContest = &toyohrpb.CoopSchedule_TeamContest{
			Stage:       r.StageID,
			MainWeapons: append([]int64(nil), r.Weapons...),
			Rewards:     append([]int64(nil), r.Rewards...),
			Attributes:  attrs,
		}
	default:
		out.Normal = &toyohrpb.CoopSchedule_Normal{
			Stage:        r.StageID,
			Boss:         r.Boss,
			MainWeapons:  append([]int64(nil), r.Weapons...),
			RewardType:   reward.Kind,
			RewardGearId: reward.ID,
			Attributes:   attrs,
		}
	}
	return out
}

// coopKumaWeaponsAttribute builds a shift's attributes MapValue with the two
// keys both preserved working Nextendo hall captures always carry:
//
//   - "KumaWeapons": the shift's Grizzco weapon, as an array_value -- not
//     the CoopSchedule_Normal/BigRun/TeamContest.kuma_weapon scalar field
//     this project previously set. Every observed working capture entry's
//     kuma_weapon field is absent, not merely zero, which is why it is
//     dropped here entirely rather than sent alongside the attribute.
//     One captured entry carried eight IDs while the others carried one.
//     The captures establish that variation, but not the selection rule, so
//     GRID0+ advertises the one weapon already selected for each generated
//     shift rather than inventing a wildcard policy.
//
//   - "CoopUsedRewardGears": the capture-derived gear pairs from
//     coop_reward_gears.go -- present on every populated CoopSchedule in
//     both captures. The
//     hall investigation found the console never attempts the
//     GameRecord-document-read phase that follows a schedule fetch in both
//     working captures; its absence was a candidate for why. Per-shift
//     variation remains unmodelled.
func coopKumaWeaponsAttribute(kumaWeaponID int64, weapons []int64) *common.MapValue {
	kuma := []*common.Value{{ValueType: &common.Value_IntegerValue{IntegerValue: kumaWeaponID}}}
	// A shift with a random slot lists the whole Grizzco pool, as the one
	// captured shift with a -1 slot did.
	for _, id := range weapons {
		if coopRandomSlot(id) {
			kuma = kuma[:0]
			for _, w := range CoopKumaWeapons {
				kuma = append(kuma, &common.Value{ValueType: &common.Value_IntegerValue{IntegerValue: w.ID}})
			}
			break
		}
	}
	gears := coopUsedRewardGears[:min(coopScheduleRewardGearCount, len(coopUsedRewardGears))]
	gearValues := make([]*common.Value, len(gears))
	for i, g := range gears {
		gearValues[i] = &common.Value{ValueType: &common.Value_MapValue{MapValue: &common.MapValue{
			Fields: map[string]*common.Value{
				"GearID":   {ValueType: &common.Value_IntegerValue{IntegerValue: int64(g.ID)}},
				"GearKind": {ValueType: &common.Value_StringValue{StringValue: g.Kind}},
			},
		}}}
	}
	return &common.MapValue{Fields: map[string]*common.Value{
		"KumaWeapons": {ValueType: &common.Value_ArrayValue{ArrayValue: &common.ArrayValue{
			Values: kuma,
		}}},
		"CoopUsedRewardGears": {ValueType: &common.Value_ArrayValue{ArrayValue: &common.ArrayValue{
			Values: gearValues,
		}}},
	}}
}

// Keep Timestamp out of the ETag as an independent publication-time field.
// It is stable for each shift, but the ETag identifies the rotation content.
func coopScheduleEtag(schedules []*toyohrpb.CoopSchedule) (string, error) {
	h := sha1.New()
	for _, schedule := range schedules {
		stable := proto.Clone(schedule).(*toyohrpb.CoopSchedule)
		stable.Timestamp = nil
		encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(stable)
		if err != nil {
			return "", fmt.Errorf("marshal Coop schedule for ETag: %w", err)
		}
		h.Write(encoded)
	}
	return quotedScheduleEtag(h.Sum(nil)), nil
}

// coopScheduleKind is CoopSchedule field 2 for a shift kind; see
// coopScheduleProto.
func coopScheduleKind(kind string) int32 {
	switch kind {
	case "big_run":
		return 2
	case "team_contest":
		return 3
	}
	return 1
}

// bigRunParams is attributes.BigRunParams, the map the CoopSchedule parser
// (main+0x32a28d0) reads for a Big Run: RewardBorders.{Gold,Silver,Bronze}
// as integer_value, each positive or the shift is dropped, and IsSpecial as
// a boolean.
func bigRunParams(r db.CoopShift) *common.Value {
	var b [3]int64
	copy(b[:], r.RewardBorders)
	integer := func(n int64) *common.Value {
		return &common.Value{ValueType: &common.Value_IntegerValue{IntegerValue: n}}
	}
	borders := &common.MapValue{Fields: map[string]*common.Value{
		"Gold": integer(b[0]), "Silver": integer(b[1]), "Bronze": integer(b[2]),
	}}
	return &common.Value{ValueType: &common.Value_MapValue{MapValue: &common.MapValue{Fields: map[string]*common.Value{
		"RewardBorders": {ValueType: &common.Value_MapValue{MapValue: borders}},
		"IsSpecial":     {ValueType: &common.Value_BooleanValue{BooleanValue: r.IsSpecial}},
	}}}}
}
