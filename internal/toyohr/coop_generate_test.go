package toyohr

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestGenerateCoopShiftsProducesOnlyValidShifts(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	from := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	shifts := GenerateCoopShifts(rng, from, 20, DefaultCoopShiftDuration)

	if len(shifts) != 20 {
		t.Fatalf("got %d shifts, want 20", len(shifts))
	}
	for i, s := range shifts {
		if err := s.Validate(); err != nil {
			t.Errorf("shift %d failed Validate: %v (%+v)", i, err, s)
		}
		if s.Kind != CoopShiftNormal {
			t.Errorf("shift %d kind = %v, want CoopShiftNormal", i, s.Kind)
		}
	}
}

// TestGenerateCoopShiftsAreBackToBack checks the schedule has no gaps and no
// overlaps -- shift N's End is shift N+1's Start.
func TestGenerateCoopShiftsAreBackToBack(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	from := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	shifts := GenerateCoopShifts(rng, from, 5, DefaultCoopShiftDuration)

	for i := 1; i < len(shifts); i++ {
		if !shifts[i].Start.Equal(shifts[i-1].End) {
			t.Errorf("shift %d starts at %s, want it to equal shift %d's end %s",
				i, shifts[i].Start, i-1, shifts[i-1].End)
		}
	}
}

func TestGenerateCoopShiftsNeverUsesABigRunStage(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	from := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	shifts := GenerateCoopShifts(rng, from, 50, DefaultCoopShiftDuration)

	for _, s := range shifts {
		stage, ok := CoopStageByID(s.StageID)
		if !ok || stage.BigRun {
			t.Errorf("shift used stage %d (%+v), want a real non-Big-Run stage", s.StageID, stage)
		}
	}
}

func TestGenerateCoopShiftsIsDeterministicForAGivenSeed(t *testing.T) {
	from := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	a := GenerateCoopShifts(rand.New(rand.NewPCG(42, 42)), from, 10, DefaultCoopShiftDuration)
	b := GenerateCoopShifts(rand.New(rand.NewPCG(42, 42)), from, 10, DefaultCoopShiftDuration)

	for i := range a {
		if a[i].StageID != b[i].StageID || a[i].Weapons != b[i].Weapons ||
			a[i].KumaWeaponID != b[i].KumaWeaponID || a[i].Boss != b[i].Boss {
			t.Fatalf("shift %d differs between two runs with the same seed:\n%+v\n%+v", i, a[i], b[i])
		}
	}
}

// TestGenerateCoopShiftsRerollReducesImmediateStageRepeats is a statistical
// check that the reroll in GenerateCoopShifts actually does something:
// with 7 real non-Big-Run stages, pure IID sampling would repeat the
// previous shift's stage about 1/7 (~14.3%) of the time. A single reroll on
// a repeat should push that meaningfully lower without eliminating it
// entirely (a repeat surviving the reroll is still possible).
func TestGenerateCoopShiftsRerollReducesImmediateStageRepeats(t *testing.T) {
	from := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	shifts := GenerateCoopShifts(rand.New(rand.NewPCG(7, 7)), from, 4000, DefaultCoopShiftDuration)

	repeats := 0
	for i := 1; i < len(shifts); i++ {
		if shifts[i].StageID == shifts[i-1].StageID {
			repeats++
		}
	}
	rate := float64(repeats) / float64(len(shifts)-1)
	// Comfortably below the ~14.3% no-reroll baseline, comfortably above 0
	// (a reroll can still land on the same stage) -- wide enough not to be
	// flaky, narrow enough to fail if the reroll were removed.
	if rate >= 0.10 {
		t.Errorf("immediate-repeat rate = %.3f, want well under the ~0.143 no-reroll baseline", rate)
	}
}
