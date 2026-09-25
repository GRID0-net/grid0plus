package toyohr

import (
	"math/rand/v2"
	"time"
)

// CoopRewardGear is one real gear piece Salmon Run can hand out as a shift
// reward.
type CoopRewardGear struct {
	ID   int32
	Kind string // "Head", "Clothes" or "Shoes" -- CoopUsedRewardGears' own GearKind values, confirmed from a real capture.
}

// CoopRewardGears is every piece Salmon Run hands out as a monthly reward,
// read from Leanny/splat3's data/mush/1100/GearInfo{Head,Clothes,Shoes}.json
// (v11.0.0 tables, the dump vs_ids.go/coop_ids.go also use).
//
// The rule: a row whose __RowId is Grizzco's own "COP" brand and whose
// HowToGet is "Other". The other COP rows are "Uroko" -- bought with scales
// in the Grizzco shop, never a shift reward. Every id in the working
// captures' CoopUsedRewardGears (coopUsedRewardGears below) is in this
// table, which is the check that the rule is right. The whole catalog is 24
// pieces; ordinary gear is never a Salmon Run reward.
//
// Ids repeat across kinds (21011 is both a hat and a shirt), so a gear is
// the (ID, Kind) pair, never the ID alone.
var CoopRewardGears = []CoopRewardGear{
	{ID: 21010, Kind: "Head"},    // Hed_COP110
	{ID: 21011, Kind: "Head"},    // Hed_COP111
	{ID: 21012, Kind: "Head"},    // Hed_COP112
	{ID: 21013, Kind: "Head"},    // Hed_COP113
	{ID: 21014, Kind: "Head"},    // Hed_COP114
	{ID: 21015, Kind: "Head"},    // Hed_COP115
	{ID: 21016, Kind: "Head"},    // Hed_COP116
	{ID: 21017, Kind: "Head"},    // Hed_COP117
	{ID: 21018, Kind: "Head"},    // Hed_COP118
	{ID: 21019, Kind: "Head"},    // Hed_COP119
	{ID: 21020, Kind: "Head"},    // Hed_COP120
	{ID: 21021, Kind: "Head"},    // Hed_COP121
	{ID: 21022, Kind: "Head"},    // Hed_COP122
	{ID: 21023, Kind: "Head"},    // Hed_COP123
	{ID: 21011, Kind: "Clothes"}, // Clt_COP111
	{ID: 21012, Kind: "Clothes"}, // Clt_COP112
	{ID: 21013, Kind: "Clothes"}, // Clt_COP113
	{ID: 21014, Kind: "Clothes"}, // Clt_COP114
	{ID: 21015, Kind: "Clothes"}, // Clt_COP115
	{ID: 21016, Kind: "Clothes"}, // Clt_COP116
	{ID: 21017, Kind: "Clothes"}, // Clt_COP117
	{ID: 21007, Kind: "Shoes"},   // Shs_COP107
	{ID: 21008, Kind: "Shoes"},   // Shs_COP108
	{ID: 21009, Kind: "Shoes"},   // Shs_COP109
}

// coopUsedRewardGears is the capture-derived advertised catalog used in
// VsParams and CoopSchedule attributes, separate from a shift's single
// reward. Both preserved captures agree on these first eleven pairs and on
// the 23-pair list for the first Coop entry. Other Coop entries vary, so this
// stable list reproduces the observed shape and known IDs without claiming to
// reproduce Nintendo's per-shift selection.
var coopUsedRewardGears = []CoopRewardGear{
	{ID: 21010, Kind: "Head"},
	{ID: 21011, Kind: "Head"},
	{ID: 21015, Kind: "Head"},
	{ID: 21013, Kind: "Head"},
	{ID: 21012, Kind: "Head"},
	{ID: 21014, Kind: "Head"},
	{ID: 21012, Kind: "Clothes"},
	{ID: 21016, Kind: "Head"},
	{ID: 21011, Kind: "Clothes"},
	{ID: 21017, Kind: "Head"},
	{ID: 21018, Kind: "Head"},
	{ID: 21013, Kind: "Clothes"},
	{ID: 21014, Kind: "Clothes"},
	{ID: 21008, Kind: "Shoes"},
	{ID: 21020, Kind: "Head"},
	{ID: 21016, Kind: "Clothes"},
	{ID: 21007, Kind: "Shoes"},
	{ID: 21015, Kind: "Clothes"},
	{ID: 21021, Kind: "Head"},
	{ID: 21009, Kind: "Shoes"},
	{ID: 21017, Kind: "Clothes"},
	{ID: 21022, Kind: "Head"},
	{ID: 21023, Kind: "Head"},
}

// The two preserved working hall captures carry 23 reward candidates in each
// CoopSchedule and 11 in VsParams.
const (
	coopScheduleRewardGearCount = 23
	vsParamsRewardGearCount     = 11
)

// coopRewardForShift fills a missing reward with one piece from
// CoopRewardGears, picked at random but seeded by the shift's slot, so every
// read of the same shift -- and every restart -- names the same reward.
// Both working hall captures set reward_type and reward_gear_id on every
// Normal shift; which piece Nintendo picks is its own policy, this is ours.
// A nonzero operator-selected id is preserved. If its category is ambiguous
// (the id exists under two kinds) or unknown, leave its type unset rather
// than invent it.
func coopRewardForShift(selected int32, start time.Time) CoopRewardGear {
	if selected != 0 {
		var kind string
		for _, gear := range CoopRewardGears {
			if gear.ID != selected {
				continue
			}
			if kind != "" && kind != gear.Kind {
				return CoopRewardGear{ID: selected}
			}
			kind = gear.Kind
		}
		return CoopRewardGear{ID: selected, Kind: kind}
	}
	slot := start.UTC().Unix() / int64(DefaultCoopShiftDuration/time.Second)
	rng := rand.New(rand.NewPCG(uint64(slot), 0x636f6f70726577)) // "cooprew"
	return CoopRewardGears[rng.IntN(len(CoopRewardGears))]
}
