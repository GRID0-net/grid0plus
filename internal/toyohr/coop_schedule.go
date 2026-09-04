package toyohr

import (
	"fmt"
	"time"
)

// CoopShiftKind is which of Salmon Run's three modes a shift belongs to.
//
// TeamContest (Eggstra Work) has no authoring path here yet -- CoopSchedule
// carries it (proto/toyohr/v1/resources.pb.go's CoopSchedule_TeamContest),
// but nothing in this pass builds or validates one. Adding it is the same
// shape of work as BigRun was; see TODO.md.
type CoopShiftKind int

const (
	// CoopShiftNormal is the ordinary Salmon Run rotation.
	CoopShiftNormal CoopShiftKind = iota
	// CoopShiftBigRun is a Big Run event.
	CoopShiftBigRun
)

func (k CoopShiftKind) String() string {
	switch k {
	case CoopShiftBigRun:
		return "big_run"
	default:
		return "normal"
	}
}

// ParseCoopShiftKind parses "normal" or "big_run", the same strings
// CoopShiftKind.String produces -- what the CLI and the store both use, so
// there is exactly one place the two spellings are decided.
func ParseCoopShiftKind(s string) (CoopShiftKind, error) {
	switch s {
	case "normal":
		return CoopShiftNormal, nil
	case "big_run":
		return CoopShiftBigRun, nil
	default:
		return 0, fmt.Errorf("coop shift kind %q must be \"normal\" or \"big_run\"", s)
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

	// Rewards are a Big Run's scale-tier reward values
	// (CoopSchedule_BigRun.Rewards). Empty for CoopShiftNormal. Their exact
	// meaning is unconfirmed -- see docs/QUESTIONS.md -- so nothing here
	// validates their contents, only that a Normal shift has none.
	Rewards []int64
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
	case CoopShiftNormal:
		if stage.BigRun {
			return fmt.Errorf("coop shift is normal but stage %d (%s) is a Big Run stage",
				c.StageID, stage.Name)
		}
		if len(c.Rewards) != 0 {
			return fmt.Errorf("coop shift is normal but carries %d Big Run reward tier(s); "+
				"Rewards only applies to a big_run shift", len(c.Rewards))
		}
	default:
		return fmt.Errorf("coop shift kind %d is neither CoopShiftNormal nor CoopShiftBigRun", c.Kind)
	}

	if !CoopBossValid(c.Boss) {
		return fmt.Errorf("coop shift boss %q is not a real king salmonid codename; "+
			"see toyohr.CoopBosses", c.Boss)
	}

	seen := make(map[int64]bool, len(c.Weapons))
	for i, id := range c.Weapons {
		if _, ok := CoopWeaponByID(id); !ok {
			return fmt.Errorf("coop shift weapon %d (slot %d) is not a real Salmon Run weapon; "+
				"see toyohr.CoopWeapons", id, i)
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
