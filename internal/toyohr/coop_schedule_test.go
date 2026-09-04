package toyohr

import (
	"strings"
	"testing"
	"time"
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
	if err == nil || !strings.Contains(err.Error(), "Rewards only applies to a big_run shift") {
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

func TestParseCoopShiftKindRoundTripsWithString(t *testing.T) {
	for _, k := range []CoopShiftKind{CoopShiftNormal, CoopShiftBigRun} {
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
