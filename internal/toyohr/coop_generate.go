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

// grizzcoWeaponChance is GRID0+'s own policy, not a Nintendo value: the
// per-slot chance a main weapon slot gets a Grizzco weapon (CoopKumaWeapons)
// instead of an ordinary one (CoopWeapons).
//
// Nothing public documents a mechanism, sentinel id, or frequency for Nintendo
// mixing a Grizzco weapon into the normal four-slot rotation -- what a real
// server does here, if anything, is not public.
//
// A real, separate mechanic does exist for one weapon slot: Nintendo's own
// schedule sometimes marks a slot "?" (a wildcard, revealed only when the match
// hands out weapons) and assigns one "rare weapon" a wildcard slot can loan for
// that rotation (Inkipedia's Salmon Run Next Wave page documents it). This is NOT
// what grizzcoWeaponChance implements: there is no known NPLN wire shape for a
// wildcard-slot flag or a per-rotation rare-weapon field in CoopSchedule_Normal
// (Stage/Boss/MainWeapons/KumaWeapon/RewardGearId only), so implementing the real
// mechanic would mean inventing a shape.
//
// Disabled (0.0), left in code rather than deleted: a hardware test went silent
// -- no console request after the schedule RPCs answered, no server-side error
// either -- while the live-generated shift active at that moment carried TWO
// Grizzco weapons in the four main slots at once (Grizzco Brella and Grizzco
// Splatana). Correlation, not a confirmed root cause, but it is the newest, most
// speculative piece of content in that response. Re-enable only after another
// hardware test clears it, or it is replaced with a confirmed shape.
const grizzcoWeaponChance = 0.0

// normalCoopStages is CoopStages with BigRun entries excluded -- the pool
// every Normal-shift generator (batch and live) draws its stage from.
var normalCoopStages = func() []CoopStage {
	var out []CoopStage
	for _, s := range CoopStages {
		if !s.BigRun {
			out = append(out, s)
		}
	}
	return out
}()

// GenerateCoopShifts builds count sequential, back-to-back Normal shifts
// starting at from (aligned to the two-hour slot grid), each lasting
// duration.
//
// What this is NOT: a discovered Nintendo algorithm. No public source this
// project has found documents how Nintendo's own server actually picks a
// rotation's stage and weapons -- only that it does, on the 40-hour cadence
// above. This is GRID0+'s own generator, offered because hand-picking
// every future shift is tedious, not because it reproduces Nintendo's real
// selection logic. The one rule it applies beyond "pick something real" is
// avoiding an immediate stage repeat between consecutive generated shifts,
// which costs nothing to guarantee and reads better than letting one
// recur by chance. Weapon and boss selection follow coopWeaponsFor and
// coopBossFor -- the same GRID0+-own policy the live generator
// (coop_live.go) uses, so a hand-triggered `switchnetctl coop generate` and
// an auto-filled live slot never look like two different generators.
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

	start := AlignToSlot(from)
	out := make([]CoopShift, 0, count)
	var previousStage int32 = -1

	for i := 0; i < count; i++ {
		stage := normalCoopStages[rng.IntN(len(normalCoopStages))]
		// Reroll once on an immediate repeat rather than looping until a
		// different one turns up: with seven stages, one reroll already
		// makes a repeat rare, and an unbounded loop is one bad seed away
		// from spinning forever for no real benefit.
		if stage.ID == previousStage {
			stage = normalCoopStages[rng.IntN(len(normalCoopStages))]
		}
		previousStage = stage.ID

		weapons := coopWeaponsFor(rng)
		kuma := CoopKumaWeapons[rng.IntN(len(CoopKumaWeapons))]
		boss := coopBossFor(rng)

		shiftStart := start.Add(time.Duration(i) * duration)
		shift := CoopShift{
			Kind: CoopShiftNormal, StageID: stage.ID, Boss: boss,
			KumaWeaponID: kuma.ID,
			Start:        shiftStart, End: shiftStart.Add(duration),
		}
		copy(shift.Weapons[:], weapons[:])
		out = append(out, shift)
	}
	return out
}

// coopWeaponsFor draws four distinct main-weapon ids, each slot
// independently a grizzcoWeaponChance roll between CoopKumaWeapons and
// CoopWeapons. See grizzcoWeaponChance's own comment for why this is a
// GRID0+ policy, not a Nintendo mechanic.
//
// Capped at 200 attempts rather than looping unbounded: with 8 Grizzco ids
// and CoopWeapons' own larger pool, a collision run long enough to hit this
// is not realistically reachable, but an unbounded loop is still one bad
// seed away from spinning forever for no benefit -- the same reasoning
// GenerateCoopShifts' own single-reroll comment already gives for stages.
// Hitting the cap falls back to the first id in CoopWeapons not already
// used, which is always real and always distinct.
func coopWeaponsFor(rng *rand.Rand) [4]int64 {
	var out [4]int64
	seen := make(map[int64]bool, 4)
	for i := 0; i < 4; i++ {
		id, ok := int64(0), false
		for attempt := 0; attempt < 200; attempt++ {
			var candidate int64
			if rng.Float64() < grizzcoWeaponChance {
				candidate = CoopKumaWeapons[rng.IntN(len(CoopKumaWeapons))].ID
			} else {
				candidate = CoopWeapons[rng.IntN(len(CoopWeapons))].ID
			}
			if !seen[candidate] {
				id, ok = candidate, true
				break
			}
		}
		if !ok {
			for _, w := range CoopWeapons {
				if !seen[w.ID] {
					id = w.ID
					break
				}
			}
		}
		seen[id] = true
		out[i] = id
	}
	return out
}

// coopBossFor picks which king salmonid this shift's Xtrawave would be.
//
// Always a real boss, never "" -- whether the Xtrawave actually triggers in
// a given match is decided by each player's Salmometer (gold eggs collected
// across waves 1-3), simulated entirely on the consoles hosting that match:
// internal/matchmaking/README.md's "Gameplay is peer-to-peer" is why
// GRID0+'s own server is never in a position to see, let alone decide,
// that outcome -- the same is true of a real Nintendo server, since the
// architecture is identical. CoopShift.Boss models which salmonid is
// eligible for this shift, a real piece of schedule data; CoopBossValid
// still accepts "" for the operator's own hand-authored shifts (Validate
// has no reason to forbid it), but nothing this package generates produces
// one.
func coopBossFor(rng *rand.Rand) string {
	return CoopBosses[rng.IntN(len(CoopBosses))]
}
