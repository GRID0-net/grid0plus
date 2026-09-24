package toyohr

import (
	"context"
	"strings"
	"testing"
	"time"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

func validNormalShift() CoopShift {
	return CoopShift{
		Kind:         CoopShiftNormal,
		Start:        time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC),
		End:          time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC),
		StageID:      1, // Spawning Grounds, not a Big Run stage
		Boss:         "SakeJaw",
		Weapons:      [4]int64{20000, 20010, 20020, 20030},
		KumaWeaponID: 20900,
	}
}

func validBigRunShift() CoopShift {
	s := validNormalShift()
	s.Kind = CoopShiftBigRun
	s.StageID = 100 // Wahoo World, a Big Run stage
	s.Rewards = []int64{100, 200, 300}
	return s
}

func validTeamContestShift() CoopShift {
	s := validNormalShift()
	s.Kind = CoopShiftTeamContest
	s.Boss = ""
	s.RewardGearID = 0
	s.Rewards = []int64{50, 100}
	return s
}

func TestCoopShiftValidateAcceptsARealNormalShift(t *testing.T) {
	if err := validNormalShift().Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestCoopShiftValidateAcceptsARealBigRunShift(t *testing.T) {
	if err := validBigRunShift().Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestCoopShiftValidateAcceptsARealTeamContestShift(t *testing.T) {
	if err := validTeamContestShift().Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestCoopShiftValidateRejectsATeamContestShiftOnABigRunStage(t *testing.T) {
	s := validTeamContestShift()
	s.StageID = 100 // Wahoo World, Big Run only
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "Big Run stage") {
		t.Errorf("Validate() = %v, want it to name the Big Run stage mismatch", err)
	}
}

func TestCoopShiftValidateRejectsABossOnATeamContestShift(t *testing.T) {
	s := validTeamContestShift()
	s.Boss = "SakeJaw"
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "no boss field") {
		t.Errorf("Validate() = %v, want it to reject a boss on a team_contest shift", err)
	}
}

func TestCoopShiftValidateRejectsARewardGearIDOnATeamContestShift(t *testing.T) {
	s := validTeamContestShift()
	s.RewardGearID = 1
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "no reward_gear_id field") {
		t.Errorf("Validate() = %v, want it to reject a reward gear id on a team_contest shift", err)
	}
}

func TestCoopShiftValidateAllowsRewardsOnATeamContestShift(t *testing.T) {
	s := validTeamContestShift()
	s.Rewards = []int64{10, 20, 30}
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() = %v, want Rewards to be allowed on a team_contest shift", err)
	}
}

func TestCoopShiftValidateRejectsANormalShiftOnABigRunStage(t *testing.T) {
	s := validNormalShift()
	s.StageID = 100 // Wahoo World, Big Run only
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "Big Run stage") {
		t.Errorf("Validate() = %v, want it to name the Big Run stage mismatch", err)
	}
}

func TestCoopShiftValidateRejectsABigRunShiftOnANormalStage(t *testing.T) {
	s := validBigRunShift()
	s.StageID = 1 // Spawning Grounds, not Big Run
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "not a Big Run stage") {
		t.Errorf("Validate() = %v, want it to name the Big Run stage mismatch", err)
	}
}

func TestCoopShiftValidateRejectsAnUnknownStageID(t *testing.T) {
	s := validNormalShift()
	s.StageID = 999999
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "999999") {
		t.Errorf("Validate() = %v, want it to name the bad stage id", err)
	}
}

func TestCoopShiftValidateRejectsAnUnknownWeapon(t *testing.T) {
	s := validNormalShift()
	s.Weapons[2] = 1 // not a real coop weapon id
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "not a real Salmon Run weapon") {
		t.Errorf("Validate() = %v, want it to name the bad weapon", err)
	}
}

func TestCoopShiftValidateRejectsARepeatedWeapon(t *testing.T) {
	s := validNormalShift()
	s.Weapons[1] = s.Weapons[0]
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("Validate() = %v, want it to name the repeat", err)
	}
}

func TestCoopShiftValidateRejectsAnUnknownKumaWeapon(t *testing.T) {
	s := validNormalShift()
	s.KumaWeaponID = 1 // not a Grizzco weapon
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "not a real Grizzco weapon") {
		t.Errorf("Validate() = %v, want it to name the bad kuma weapon", err)
	}
}

func TestCoopShiftValidateRejectsAnUnknownBoss(t *testing.T) {
	s := validNormalShift()
	s.Boss = "TotallyMadeUpBoss"
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "king salmonid") {
		t.Errorf("Validate() = %v, want it to name the bad boss", err)
	}
}

func TestCoopShiftValidateAllowsNoBoss(t *testing.T) {
	s := validNormalShift()
	s.Boss = ""
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() = %v, want an empty boss to be allowed", err)
	}
}

func TestCoopShiftValidateRejectsRewardsOnANormalShift(t *testing.T) {
	s := validNormalShift()
	s.Rewards = []int64{100}
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "Rewards only applies to a big_run or team_contest shift") {
		t.Errorf("Validate() = %v, want it to reject Rewards on a normal shift", err)
	}
}

func TestCoopShiftValidateRejectsUnalignedStart(t *testing.T) {
	s := validNormalShift()
	s.Start = s.Start.Add(time.Hour) // one hour off the two-hour slot
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "slot boundary") {
		t.Errorf("Validate() = %v, want it to name the slot-alignment failure", err)
	}
}

func TestCoopShiftValidateRejectsEndBeforeStart(t *testing.T) {
	s := validNormalShift()
	s.End = s.Start.Add(-2 * time.Hour)
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "not after its start") {
		t.Errorf("Validate() = %v, want it to name the end-before-start failure", err)
	}
}

func TestCoopShiftIDIsDerivedFromKindAndStart(t *testing.T) {
	s := validNormalShift()
	if got, want := s.ID(), "normal-1788566400"; got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}
	big := validBigRunShift()
	if got, want := big.ID(), "big_run-1788566400"; got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}
	if s.ID() == big.ID() {
		t.Error("a normal and a big_run shift starting at the same time got the same ID")
	}
}

// TestTeamContestRendersOnItsOwnWireSubMessage asserts on the actual
// CoopSchedule the console would receive, not on the Go struct that produced
// it -- the same "assert on the wire" reasoning CLAUDE.md's conventions call
// for. A team_contest shift must set CoopSchedule.TeamContest and leave
// Normal/BigRun nil, and TeamContest itself carries no boss and no reward
// gear id, because CoopSchedule_TeamContest has no field for either.
func TestTeamContestRendersOnItsOwnWireSubMessage(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	svc, _ := newService(t, at)
	sc := &scheduleService{s: svc}

	shift := validTeamContestShift()
	shift.Start = at
	shift.End = at.Add(8 * time.Hour)
	if err := svc.SaveCoopShift(context.Background(), shift); err != nil {
		t.Fatalf("SaveCoopShift: %v", err)
	}

	resp, err := sc.SelectCoopSchedules(context.Background(), &toyohrpb.SelectCoopSchedulesRequest{})
	if err != nil {
		t.Fatalf("SelectCoopSchedules: %v", err)
	}
	// Other entries are the live-generated Normal rotation
	// (liveNormalCoopShifts) filling slots nothing was stored for -- this
	// test only cares about the one team_contest shift it stored.
	got := findCoopScheduleByShiftID(t, resp.GetSchedules(), shift.ID())
	if got.GetNormal() != nil {
		t.Error("a team_contest shift set CoopSchedule.Normal")
	}
	if got.GetBigRun() != nil {
		t.Error("a team_contest shift set CoopSchedule.BigRun")
	}
	tc := got.GetTeamContest()
	if tc == nil {
		t.Fatal("CoopSchedule.TeamContest is nil")
	}
	if tc.GetStage() != shift.StageID {
		t.Errorf("TeamContest.Stage = %d, want %d", tc.GetStage(), shift.StageID)
	}
	if got, want := len(tc.GetMainWeapons()), 4; got != want {
		t.Errorf("TeamContest.MainWeapons has %d entries, want %d", got, want)
	}
	if int64(tc.GetKumaWeapon()) != shift.KumaWeaponID {
		t.Errorf("TeamContest.KumaWeapon = %d, want %d", tc.GetKumaWeapon(), shift.KumaWeaponID)
	}
	if got, want := tc.GetRewards(), shift.Rewards; len(got) != len(want) {
		t.Errorf("TeamContest.Rewards = %v, want %v", got, want)
	}
}

// TestCoopScheduleNameCarriesATargetsSegment is the regression test for a
// real crash: a 2026-09-24 decompile of Splatoon 3 v11.3.0 found the
// client's resource-name parser for a CoopSchedule requires the literal
// shape "tenants/<t>/targets/<target>/coopSchedules/<rest>" and hard-aborts
// when a name does not parse that way. The old shape here,
// "tenants/<t>/coopSchedules/<id>", omitted the "targets/<target>" segment
// entirely and reproduced on SwitchNet's own hardware with four real coop
// shifts seeded.
func TestCoopScheduleNameCarriesATargetsSegment(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	svc, _ := newService(t, at)
	sc := &scheduleService{s: svc}

	shift := validNormalShift()
	shift.Start = at
	shift.End = at.Add(8 * time.Hour)
	if err := svc.SaveCoopShift(context.Background(), shift); err != nil {
		t.Fatalf("SaveCoopShift: %v", err)
	}

	resp, err := sc.SelectCoopSchedules(context.Background(), &toyohrpb.SelectCoopSchedulesRequest{})
	if err != nil {
		t.Fatalf("SelectCoopSchedules: %v", err)
	}

	name := findCoopScheduleByShiftID(t, resp.GetSchedules(), shift.ID()).GetName()
	if !strings.Contains(name, "/targets/") || !strings.Contains(name, "/coopSchedules/") {
		t.Errorf("name = %q, want a targets/<target>/coopSchedules/<id> shape -- this is what crashes a real console", name)
	}
	// "targets" must land as its own path segment, not merely appear as a
	// substring (e.g. inside a shift id) -- the client's parser walks
	// segments by '/', not by substring search.
	segs := strings.Split(strings.TrimPrefix(name, svc.tenantPath()+"/"), "/")
	if len(segs) < 4 || segs[0] != "targets" || segs[2] != "coopSchedules" {
		t.Errorf("name segments = %v, want [targets <target> coopSchedules <id>]", segs)
	}
}

func TestParseCoopShiftKindRoundTripsWithString(t *testing.T) {
	for _, k := range []CoopShiftKind{CoopShiftNormal, CoopShiftBigRun, CoopShiftTeamContest} {
		parsed, err := ParseCoopShiftKind(k.String())
		if err != nil {
			t.Fatalf("ParseCoopShiftKind(%q): %v", k.String(), err)
		}
		if parsed != k {
			t.Errorf("ParseCoopShiftKind(%q) = %v, want %v", k.String(), parsed, k)
		}
	}
}

func TestParseCoopShiftKindRejectsGarbage(t *testing.T) {
	if _, err := ParseCoopShiftKind("bigrun"); err == nil {
		t.Error("expected an error for an unrecognised kind string")
	}
}

// findCoopScheduleByShiftID picks out one entry from a SelectCoopSchedules
// response by its ShiftId -- since liveNormalCoopShifts now fills in Normal
// slots nothing was stored for, a response can legitimately carry entries a
// test did not itself store.
func findCoopScheduleByShiftID(t *testing.T, schedules []*toyohrpb.CoopSchedule, id string) *toyohrpb.CoopSchedule {
	t.Helper()
	for _, s := range schedules {
		if s.GetShiftId() == id {
			return s
		}
	}
	t.Fatalf("no schedule with shift id %q among %d schedules", id, len(schedules))
	return nil
}
