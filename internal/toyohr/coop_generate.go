package toyohr

import (
	"math/rand/v2"
	"time"
)

// DefaultCoopShiftDuration is how long one Salmon Run rotation shift lasts on
// a real server.
//
// Confirmed against two independent public sources (Game8's Splatoon 3
// stage-list guide and a second, separately-sourced search result), not
// guessed: Salmon Run's stage and weapon rotation changes every 40 hours,
// distinct from Big Run's own 48-hour window (Inkipedia's "List of Big Runs"
// page). Nothing here derives this from the game's own files the way
// coop_ids.go's ids are -- it is real, publicly observable calendar
// behaviour, the same category of fact as a Splatfest's own phase offsets
// in fest.go, not a wire-protocol guess CLAUDE.md rule 6 would apply to.
const DefaultCoopShiftDuration = 40 * time.Hour

// GenerateCoopShifts builds count sequential, back-to-back Normal shifts
// starting at from (aligned to the two-hour slot grid), each lasting
// duration.
//
// What this is NOT: a discovered Nintendo algorithm. No public source this
// project has found documents how Nintendo's own server actually picks a
// rotation's stage and weapons -- only that it does, on the 40-hour cadence
// above. This is SwitchNet's own generator, offered because hand-picking
// every future shift is tedious, not because it reproduces Nintendo's real
// selection logic. The one rule it applies beyond "pick something real" is
// avoiding an immediate stage repeat between consecutive generated shifts,
// which costs nothing to guarantee and reads better than letting one
// recur by chance.
//
// Every value is drawn from coop_ids.go's own real tables, so a generated
// shift is exactly as valid as a hand-picked one -- Validate never rejects
// what this produces. Big Run is deliberately out of scope: it is a
// calendar event with a small, real, publicly-tracked history
// (internal/toyohr/coop_ids.go's CoopStages comment), not a rotation to
// synthesize entries for.
func GenerateCoopShifts(rng *rand.Rand, from time.Time, count int, duration time.Duration) []CoopShift {
	if rng == nil {
		rng = rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0))
	}

	var normalStages []CoopStage
	for _, s := range CoopStages {
		if !s.BigRun {
			normalStages = append(normalStages, s)
		}
	}

	start := AlignToSlot(from)
	out := make([]CoopShift, 0, count)
	var previousStage int32 = -1

	for i := 0; i < count; i++ {
		stage := normalStages[rng.IntN(len(normalStages))]
		// Reroll once on an immediate repeat rather than looping until a
		// different one turns up: with seven stages, one reroll already
		// makes a repeat rare, and an unbounded loop is one bad seed away
		// from spinning forever for no real benefit.
		if stage.ID == previousStage {
			stage = normalStages[rng.IntN(len(normalStages))]
		}
		previousStage = stage.ID

		weapons := randomDistinctWeapons(rng, CoopWeapons, 4)
		kuma := CoopKumaWeapons[rng.IntN(len(CoopKumaWeapons))]
		boss := CoopBosses[rng.IntN(len(CoopBosses))]

		shiftStart := start.Add(time.Duration(i) * duration)
		shift := CoopShift{
			Kind: CoopShiftNormal, StageID: stage.ID, Boss: boss,
			KumaWeaponID: kuma.ID,
			Start:        shiftStart, End: shiftStart.Add(duration),
		}
		copy(shift.Weapons[:], weapons)
		out = append(out, shift)
	}
	return out
}

// randomDistinctWeapons picks n distinct weapons from pool via a partial
// Fisher-Yates shuffle, returning just their ids -- the standard way to draw
// several unique items from a set without rejection-sampling duplicates.
func randomDistinctWeapons(rng *rand.Rand, pool []CoopWeapon, n int) []int64 {
	shuffled := append([]CoopWeapon(nil), pool...)
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	out := make([]int64, n)
	for i := 0; i < n; i++ {
		out[i] = shuffled[i].ID
	}
	return out
}
