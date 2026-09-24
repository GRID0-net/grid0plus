# CoopSchedule's Grizzco weapon lives in attributes, not kuma_weapon

Both preserved working Nextendo hall captures carry every
`CoopSchedule_Normal`/`_BigRun`/`_TeamContest` entry's Grizzco weapon inside
`attributes.KumaWeapons` -- an `array_value` of real `CoopKumaWeapons` ids --
never in the `kuma_weapon` scalar field (field 4/5 depending on the
sub-message). The scalar field is absent from every entry in both captures,
not merely zero.

Almost every observed entry carries exactly one id in `KumaWeapons`. One
entry in `nextendo-hall-20260923T205738Z` carries all eight real
`CoopKumaWeapons` ids at once
(`20900, 21900, 22900, 23900, 25900, 26900, 27900, 28900`) instead of a
single one -- plausibly the real "?" wildcard/all-random weapon mechanic
(Inkipedia's Salmon Run Next Wave page, researched earlier this session:
Nintendo sometimes runs an all-wildcard rotation where every main-weapon
slot, and evidently the Grizzco slot too, draws from a pool rather than a
fixed id). Never confirmed beyond this one example, and not reproduced here.

`internal/toyohr/coop_schedule.go`'s `coopKumaWeaponsAttribute` now sets
`attributes.KumaWeapons` to the shift's single `KumaWeaponID`, and the
`kuma_weapon` scalar field is no longer set at all -- matching every
observed working entry's absence, not merely zero (this project's own
convention: assert an absence only when a capture actually shows it
absent).

This was found while investigating why the hall disconnected after two
2026-09-24 hardware crashes near Salmon Run content; `liveCoopFillEnabled`
(schedule_preview.go) was disabled as a result and is now re-enabled with
this fix in place. Whether this specific mismatch was the actual cause of
either crash is unconfirmed -- the next hardware test with Coop content
live again is what actually answers that.

`CoopUsedRewardGears` (also inside `attributes` on every populated
`CoopSchedule` and `SelectVsParams` response -- see
`splat3-vsparams-attributes-2026-09-24.md`) is still not sent: it needs a
real Splatoon 3 gear id table this project has not built yet
(`Leanny/splat3`'s `GearInfoHead/Clothes/Shoes.json` can source one the same
way `vs_ids.go`/`coop_ids.go`'s tables were).
