package toyohr

import (
	"math/rand/v2"
	"time"
)

// coopLiveHorizonShifts is how many live-generated Normal shifts
// liveNormalCoopShifts returns: the one covering now, plus the next two --
// enough for a console to show "now" and "coming up" the way Splatoon 3's
// own Salmon Run menu does, the same reasoning scheduleHorizon's own comment
// gives for Vs, scaled down because a Coop shift is 40 hours long rather
// than 2.
const coopLiveHorizonShifts = 3

// liveNormalCoopShifts computes the Normal rotation live, the same kind of
// pure function of the clock SelectVsSchedules/vsSchedule already is for
// Turf War and Anarchy -- nothing here is read from or written to the store.
//
// Each shift is seeded from its own aligned start time (coopSlotSeed), so
// the same real moment always produces the same shift regardless of when or
// how many times it is computed: "random deterministic maps", the same
// property Vs's schedule already has by construction. This exists so a
// fresh SwitchNet install has a working Salmon Run rotation with zero
// operator setup, the same way Vs does -- switchnetctl's own
// `coop generate`/SaveCoopShift remain how an operator overrides a specific
// slot; SelectCoopSchedules only falls back to a live shift for a Normal
// slot nothing has been stored for (see its own comment).
func liveNormalCoopShifts(now time.Time, count int) []CoopShift {
	if count <= 0 {
		count = 1
	}
	duration := DefaultCoopShiftDuration
	// Every 40-hour slot boundary since the Unix epoch, which is itself
	// 2-hour-slot-aligned -- so every generated Start/End lands on
	// Splatoon 3's slot grid without needing AlignToSlot per shift.
	epoch := time.Unix(0, 0).UTC()
	index := now.UTC().Sub(epoch) / duration
	first := epoch.Add(index * duration)

	out := make([]CoopShift, 0, count)
	for i := 0; i < count; i++ {
		start := first.Add(time.Duration(i) * duration)
		out = append(out, coopShiftAtSlot(start, duration))
	}
	return out
}

// coopShiftAtSlot deterministically generates the one Normal shift starting
// at start and lasting duration, applying the same SwitchNet-own
// weapon/boss policy GenerateCoopShifts uses (coopWeaponsFor, coopBossFor).
func coopShiftAtSlot(start time.Time, duration time.Duration) CoopShift {
	rng := coopSlotRNG(start)

	stage := normalCoopStages[rng.IntN(len(normalCoopStages))]
	// Avoid an immediate repeat against the previous slot, the same rule
	// GenerateCoopShifts applies -- computed by seeding the previous slot's
	// own RNG independently rather than storing state, since this function
	// must stay a pure function of start alone.
	prevRNG := coopSlotRNG(start.Add(-duration))
	prevStage := normalCoopStages[prevRNG.IntN(len(normalCoopStages))]
	if stage.ID == prevStage.ID {
		stage = normalCoopStages[rng.IntN(len(normalCoopStages))]
	}

	weapons := coopWeaponsFor(rng)
	kuma := CoopKumaWeapons[rng.IntN(len(CoopKumaWeapons))]
	boss := coopBossFor(rng)

	shift := CoopShift{
		Kind: CoopShiftNormal, StageID: stage.ID, Boss: boss,
		KumaWeaponID: kuma.ID,
		Start:        start, End: start.Add(duration),
	}
	copy(shift.Weapons[:], weapons[:])
	return shift
}

// coopSlotRNG seeds a PRNG purely from a slot's aligned start time, so
// coopShiftAtSlot is reproducible: the same start always draws the same
// sequence of values, which is what makes the live schedule "deterministic"
// rather than re-rolled on every request or every server restart.
func coopSlotRNG(start time.Time) *rand.Rand {
	// A fixed, arbitrary salt distinguishes this seed stream from any other
	// per-time-seeded RNG in the package (none currently exist, but nothing
	// stops one being added later) -- not a security boundary, just seed
	// hygiene.
	const salt = 0x53616c6d6f6e // "Salmon" in hex, an arbitrary constant.
	return rand.New(rand.NewPCG(uint64(start.UTC().Unix()), salt))
}
