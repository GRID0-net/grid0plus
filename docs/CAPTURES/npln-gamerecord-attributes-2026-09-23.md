# GameRecord attributes: the documents InitializeAttributes creates (2026-09-23)

| | |
| --- | --- |
| Hosts | the NPLN tenant (`t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net`) |
| Title | Splatoon 3 v11.3.0, `main` build id `28C4287AEE36F7499DA60F3E68B54C70DA382D75` |
| Evidence | a decompile of the operator's own dump (nx2elf + capstone), cross-checked against what [NextendoNetwork/splatoon-3](https://github.com/NextendoNetwork/splatoon-3)'s code states as measured |
| Captured by | nobody — no packet capture exists; Nextendo's captures are not redistributed and are not used |

Not a capture. It settles *shapes* GRID0+ needs before it can answer
`nn.npln.toyohr.v1.GameRecord/InitializeAttributes` and serve the Ugcstore
documents it creates. No Nintendo data was copied: Nextendo's repository ships
no capture files, and only facts from its code comments are taken
(PolyForm Shield — facts, never code, the boundary used for it everywhere else).

## The flow (Nextendo, measured)

1. Right after auth, and again on entering the lobby, Splatoon 3 reads its
   GameRecord documents through `Ugcstore/GetDocument`.
2. A document that does not exist yet must come back **absent** — a
   two-frame NotFound. An empty `Document` message crashes the game
   (`2162-0001` in `nn.npln.Worker`).
3. Absent, the game calls `GameRecord/InitializeAttributes`, which answers
   `{attributes: MapValue, document: string}`. `document` names the document
   now holding those attributes — **not** the one that was just read — and
   that document must then exist in Ugcstore with the same fields, or the game
   re-initialises forever (measured: ~600 calls a minute, the endless ink
   screen; 1191 calls in two minutes on entering the lobby, matchmaking never
   reached).
4. The request says which record to create; the answer must be that kind.
   Field 15 `initial_coop` and field 16 `coop_shift` are Salmon Run; anything
   else (`initial_vs`, `new_season`, the per-mode markers) is Versus. A Salmon
   Run document served for a Versus request is what crashed the game in
   Nextendo's measurement.

## Document names (binary + Nextendo)

`tenants/<tenant>/documents/services/GameRecord/users/<uid>/…` — the SDK
aborts on any name not of the form `tenants/<t>/documents/<rest>` (Nextendo:
`2162-0001`, and such a name survives a restart if persisted).

| Document | Source |
| --- | --- |
| `…/VsUserAttribute/Data` | format string at `0x4F98078`, used at `0x3283838` |
| `…/CoopUserAttribute/Data` | Nextendo (the Salmon Run answer's `document`) |
| `…/VsResults/<battle>`, `…/PointCardRegular/<date>`, `…/BankaraChallenges/<id>`, `…/WeaponPowerMeasurement/<n>` | binary strings / Nextendo; not created by InitializeAttributes |

## The attribute keys, and their types (binary)

The parser at `0x327A270`–`0x327BCF8` reads each key as
`if (has(doc, key)) field = get<T>(doc, key)`: `0xA39EC` is the presence test,
and the typed getter it calls next gives the type.

| Type (getter) | Keys |
| --- | --- |
| int (`0xA41B8`) | `season_id`, `mm_disable_limit`, `udemae`, `udemae_point`, `best_udemae`, `bankara_continuation_victories`, `bankara_total_victories`, `challenge_status`, `league_power_battle_num`, `bankara_power_battle_num`, `token_expires_in`, `jewel_num_regular`, `jewel_num_challenge`, `fest_power_battle_num`, `total_grade_point`, `smell` |
| bool (`0xA40A4`) | `is_udemae_reset`, `is_promo` |
| string (`0xA44F0`) | `challenge_id`, `league_tag_id`, `bankara_tag_id`, `league_part_id`, `bankara_schedule_id`, `fest_id`, `shift_id`, `limited_shift_id` |
| double (`0xA42C8`) | `league_power_rate/rd/vol`, `best_league_power`, `bankara_power_rate/rd/vol`, `best_bankara_power` |
| per-mode helper (`0x327F030`), `%@` = mode | `x_power_initialized_`, `x_power_`, `x_power_rd_`, `x_power_vol_`, `fixed_x_power_`, `win_count_x_`, `lose_count_x_`, `x_power_status_`, `x_power_is_initial_`, `meas_id_`, `result_history_x_` |
| rating triplets, not yet typed | `mmr_initialized/rate/rd/vol_{tw,ar,gl,lf,cl}`, `x_mmr_rate/rd/vol_{ar,gl,lf,cl}`, `spr_rate/rd/vol_{tw,ar,gl,lf,cl}`, `fest_power_rate/rd/vol_{regular,challenge}` |

Groups in parser order: Versus (`season_id` … `token_expires_in`, ending at
`0x327B790`), Splatfest (`fest_id` … `fest_power_battle_num`, ending at
`0x327BA2C`), Salmon Run (`shift_id`, `limited_shift_id`, `mm_disable_limit`,
`total_grade_point`, `smell`, …).

## What this means for the answer

**No key is mandatory to this parser.** A key that is absent leaves the
field at the game's own default, so GRID0+ does not need to invent a rank,
a rating or an X Power for a new player: it can put in `attributes` only what
the request itself carries (the season id, the Salmon Run shift id, a grade
total) and let the game's defaults stand for the rest.

## Still open

- Whether another reader of these documents (outside this parser) requires a
  key. The parser above does not; Nextendo's crash came from wrong content, not
  missing content.
- The types of the rating triplets and the exact `%@` mode suffixes.
- `CoopUserAttribute/Data`'s full key list beyond the Salmon Run group above.
- Whether an `attributes` map with only the request's own values is accepted
  on hardware — only a console run can show that.
