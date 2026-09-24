# SelectVsParams.attributes: ten fixed server config values

Both preserved working Nextendo hall captures
(`nextendo-hall-20260923T205738Z`, `nextendo-second-20260923T210513Z`)
carry an identical, byte-for-byte `attributes` MapValue on every
`SelectVsParams` response, across every exchange in both captures:

```
DivideXRate: 2000
DivideXRateAtlantic: 500
DivideXRatePacific: 2000
IsAbleToSendImageWithoutSns: true
IsClampDownActive: true
IsMemHackKickActive: true
IsOnlinePenaltyActive: true
P2PKeepAliveSec: 8
PlazaFixedTimestamp: 1726452000
XRankingRegionRestrict: { Atlantic: [1], Pacific: [] }
CoopUsedRewardGears: [array of real gear id/kind pairs, 11 entries]
```

These read as fixed server configuration, not anything account- or
session-specific -- identical across two independently captured sessions,
weeks apart in the same server's lifetime, for different accounts. SwitchNet
previously sent `attributes: {}` (empty) on every `SelectVsParams` response.

`internal/toyohr/schedule.go`'s `vsParamsAttributes()` now sends the ten
non-`CoopUsedRewardGears` keys with the exact captured values. A wire-level
test (`TestSelectVsParamsAttributesMatchBothWorkingCaptures`,
`internal/toyohr/schedule_test.go`) asserts each one.

`CoopUsedRewardGears` is deliberately still absent: it needs a real
Splatoon 3 gear id table this project has not built (`Leanny/splat3`'s
`GearInfoHead/Clothes/Shoes.json` can source real ids the same way
`vs_ids.go`/`coop_ids.go`'s tables were built), not something to invent
here. The same key, with the same shape, also appears in `CoopSchedule`'s
own `attributes` field (see `internal/toyohr/schedule_preview.go`'s
`liveCoopFillEnabled` comment) -- building a real gear table would resolve
both gaps at once.

**Whether this is what has been causing the hall disconnect is unconfirmed.**
This is a correlation (a real, previously-empty field that both working
captures populate identically) fixed with the safest possible confidence
(the captured values themselves, not a guess), not a proven root cause. The
next hardware test is what actually answers that.
