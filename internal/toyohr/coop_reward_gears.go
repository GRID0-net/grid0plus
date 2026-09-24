package toyohr

import "time"

// CoopRewardGear is one real gear piece Salmon Run can hand out as a shift
// reward.
type CoopRewardGear struct {
	ID   int32
	Kind string // "Head", "Clothes" or "Shoes" -- CoopUsedRewardGears' own GearKind values, confirmed from a real capture.
}

// CoopRewardGears are real gear ids, read directly from Leanny/splat3's own
// GearInfoHead/Clothes/Shoes.json (the same public data-dump method
// vs_ids.go/coop_ids.go's tables already use), the same way every other
// real-id table in this package is sourced -- not invented.
//
// This is NOT a claim to know Nintendo's real per-shift reward-pool
// selection: a real capture's own CoopSchedule.attributes.CoopUsedRewardGears
// carries roughly two dozen entries per shift, drawn from Nintendo's own
// unknown selection logic, not the whole ~940-piece gear catalog. This
// table is a small, honestly-real subset (eight ids per Head/Clothes/Shoes,
// the first each JSON file lists) SwitchNet picks from with its own
// deterministic policy -- coopRewardGearsAttribute below -- to test whether
// this field's mere presence (found missing from every SwitchNet
// CoopSchedule response to date) is what a real console needs to proceed
// past it, not to reproduce which specific pieces a real server would
// offer.
//
// TODO: build the real full-catalog table (Leanny/splat3's
// GearInfoHead/Clothes/Shoes.json have ~940 real pieces total) and a
// defensible SwitchNet-own selection policy for which subset a given shift
// offers, the same rigor CoopStages/CoopWeapons already get. This eight-
// per-category placeholder is deliberately kept small for now -- the
// operator's own call, 2026-09-24 -- to test whether this field's mere
// presence unblocks the hall's GameRecord-document-read phase before
// investing in the full table.
var CoopRewardGears = []CoopRewardGear{
	{ID: 10003, Kind: "Head"},
	{ID: 25000, Kind: "Head"},
	{ID: 25001, Kind: "Head"},
	{ID: 25002, Kind: "Head"},
	{ID: 25003, Kind: "Head"},
	{ID: 25004, Kind: "Head"},
	{ID: 25005, Kind: "Head"},
	{ID: 25006, Kind: "Head"},
	{ID: 25000, Kind: "Clothes"},
	{ID: 25001, Kind: "Clothes"},
	{ID: 25002, Kind: "Clothes"},
	{ID: 25003, Kind: "Clothes"},
	{ID: 25004, Kind: "Clothes"},
	{ID: 25005, Kind: "Clothes"},
	{ID: 25006, Kind: "Clothes"},
	{ID: 25007, Kind: "Clothes"},
	{ID: 25000, Kind: "Shoes"},
	{ID: 25001, Kind: "Shoes"},
	{ID: 25002, Kind: "Shoes"},
	{ID: 25003, Kind: "Shoes"},
	{ID: 25004, Kind: "Shoes"},
	{ID: 25005, Kind: "Shoes"},
	{ID: 25006, Kind: "Shoes"},
	{ID: 25007, Kind: "Shoes"},
}

// The two preserved working hall captures carry 23 reward candidates in each
// CoopSchedule and 11 in VsParams. Which gear IDs Nintendo chooses is its own
// rotation; SwitchNet selects real IDs from its independent public-data table.
const (
	coopScheduleRewardGearCount = 23
	vsParamsRewardGearCount     = 11
)

// coopRewardForShift fills a missing reward with a stable, real gear pair.
// Both working hall captures set reward_type and reward_gear_id on every
// Normal shift. The public GearInfo tables confirm the three category names
// and the ids in CoopRewardGears; the slot-to-reward policy is SwitchNet's.
// A nonzero operator-selected id is preserved. If its category is ambiguous
// or absent from our small catalog, leave its type unset rather than invent it.
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
	kinds := [...]string{"Head", "Clothes", "Shoes"}
	slot := start.UTC().Unix() / int64(DefaultCoopShiftDuration/time.Second)
	kind := kinds[((slot%int64(len(kinds)))+int64(len(kinds)))%int64(len(kinds))]
	for _, gear := range CoopRewardGears {
		if gear.Kind == kind {
			return gear
		}
	}
	return CoopRewardGear{}
}
