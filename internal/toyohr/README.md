# internal/toyohr

Splatoon 3's own NPLN services (`nn.npln.toyohr.v1.*`). Splatfests first,
because custom Splatfests are what phase 2 exists for.

| | |
| --- | --- |
| Reimplements | `nn.npln.toyohr.v1.FestService`, `Schedule`, `CloudSave.GetSaveRecord` |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Definitions | [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols) `latest/proto/toyohr/v1/{fest,schedule,cloud_save}.proto` |
| Needs a database | yes, for fests and coop shifts -- the rest of `Schedule` and `GetSaveRecord` need none |

## A Splatfest lives in two places, and they must agree

This is the thing to understand before changing anything here.

```
NPLN schedule  (this package)   names a fest: its id, its pack, its revision
BCAT pack      (internal/bcat)  carries the CONTENT, and names the fest it is for
```

`SelectFestSchedule` carries **no content at all** — no team names, no banners,
no idol dialogue. Only mechanics: which pack, which revision of it, the five
phase times, and the publication window. Everything a player actually sees is
inside the BCAT pack the console fetches separately.

So the fest id, the resource name and the revision announced here must match
what the console's BCAT cache holds. When they disagree the game asks BCAT for a
resource digest it cannot find and reports **`BcatInvalid`**, which reaches the
player as a generic communication error.

## Three details that each cost somebody an evening

**Timestamps must land on two-hour boundaries.** Splatoon 3 files scheduled
things into 7200-second slots. A fest whose phases are not slot-aligned belongs
to no slot: the game stops treating it as open and starts demanding its
*verdict*, which a real console never does mid-fest. Every player's lobby fails
at once with nothing in any log.

Every phase offset is a multiple of the slot length, so aligning the start
aligns the whole table. `NewFest` aligns; `Validate` refuses — a caller asking
for "next Saturday" wants a fest, but an operator who typed a time should be
told it moved rather than have it happen quietly.

**The fest id is uppercase and the resource name is lowercase**, and they fail
differently. A lowercase id is simply a *different fest* to the game. A
capitalised resource name is worse: the console derives the pack's AES
decryption IV from that string, so one wrong capital means the pack never opens
and nothing in the failure points back at the name.

**A key in the wrong slot is silent.** `GetFestDecryptionKey` returns keys
positionally — notice, start, result, then the three per-team keys. A
misordered key is handed to the console for the wrong phase and the pack fails
to open, with nothing saying which phase was wrong.

## Seven key slots, not three

The message declares seven key fields and the request carries a `show_all_keys`
flag. A reference implementation this protocol knowledge was checked against
caps its response at three, on the grounds that the game never asks for more —
which is plausibly true of the phases it was tested against and is not a
property of the protocol. All seven are filled when supplied.

A key that was **not** supplied is left absent rather than empty. Nintendo omits
the key for a phase that has not happened yet, and an empty string is a key the
console would try to use.

## The keys are ours

Nothing here decrypts anything of Nintendo's. A fest SwitchNet serves is a fest
SwitchNet built: its pack was encrypted with keys stored alongside it, and
`GetFestDecryptionKey` hands back those same keys. That is what makes a genuinely
custom Splatfest possible rather than a rename of one of Nintendo's — both
inputs to the pack encryption, the key and the resource name, are the server's
to choose.

## Team choice is permanent, and results are honestly incomplete

`CreateFestEntry` records a caller's team for a fest -- self-scoped, the same
discipline `internal/baas`'s `requireSelf` applies to every user-scoped HTTP
endpoint: `GetFestEntry` refuses to read another account's choice
(`PermissionDenied`), matching real Splatfest rules that a pick cannot be
seen or changed by anyone else before results are public. A second
`CreateFestEntry` for the same fest and caller is `AlreadyExists`, not a
silent overwrite -- Splatfest team choice does not have an "undo" in the
real game, and letting a retried call quietly replace an earlier pick would
be inventing a feature Nintendo's own version does not have.

`GetFestResult` is **honestly incomplete, not `Unimplemented`**. Every
sub-result (`Yobisai`, `HonsaiMidterm`, `HonsaiFinal`, `Overall`) comes back
with `IsValid: false` rather than a computed ratio or point total, because
SwitchNet has nothing to compute one from: `npln_fest_entries` records who
picked which team, not who won a battle, and `GameRecord` -- the RPC that
would report battle outcomes -- is not implemented (see `TODO.md` §1.4).
Deriving a ratio from entry counts alone was considered and rejected: a
lopsided sign-up split is not the same thing as a lopsided battle result,
and showing one dressed as the other would be actively misleading rather
than merely incomplete. `IsValid` exists in the proto specifically to say
"not computed yet," so using it is the defensible reading rather than an
invented number -- the same principle `Schedule`'s honest emptiness follows,
just for a per-fest resource instead of a boot-time list. See the
`TODO(capture-needed)` on `GetFestResult` for what a capture would need to
settle before this can report anything else.

## Overlapping fests

Two fests live at once is a state the game has no reading for. The operator can
create it by mistake, so `currentFest` picks the most recently started and logs
a warning rather than picking arbitrarily.

## `Schedule`: honest emptiness for Vs, a real rotation for Coop

`schedule.go` answers `SelectVsParams`, `SelectVsSchedules`,
`SelectSeasonSchedules` and `SelectLeagueSchedules` with an empty schedule
list rather than `Unimplemented`, because the console calls these early in
boot and the alternative is a to-do-list log line where a working boot query
should be.

The content those four RPCs would normally carry — which stages, which mode
— is Nintendo's own game design data. Unlike a fest, which the operator
authors and `SaveFest` validates, there is no equivalent authoring path for a
normal Vs rotation and no dump of one. CLAUDE.md is explicit that a genuinely
undocumented shape must not be filled with an invented-but-plausible
Nintendo internal, so these four stay honestly empty (`TODO(capture-needed)`,
in `schedule.go`) rather than serving a fabricated rotation nothing here
would ever catch as wrong.

**`SelectCoopSchedules` is the fifth RPC, and it is different**, because the
usual blocker for a Vs/Coop rotation — no known wire shape — did not actually
apply here: `nn.npln.toyohr.v1.CoopSchedule`'s own decompiled proto
(`proto/toyohr/v1/resources.pb.go`) already carries `Normal`, `BigRun` and
`TeamContest` as three parallel sub-messages, each with real fields
(`stage`, `boss`, `main_weapons`, `kuma_weapon`, `reward_gear_id`, and
`BigRun`'s own `rewards`). Independently corroborated, not just read once:
[`nintendoapis/splatnet3-types`](https://github.com/nintendoapis/splatnet3-types),
a public typed schema of SplatNet 3's own GraphQL API, has the identical
three-way split (`CoopGroupingSchedule.{regularSchedules,bigRunSchedules,
teamContestSchedules}`, each entry's `setting.__typename` naming
`CoopNormalSetting` or `CoopBigRunSetting`). Two independently maintained
sources agreeing on the same shape is as much confidence as this project
gets without a console of its own.

What was actually missing was **content and an authoring path** — real
stage/weapon/boss ids, and something to play `SaveFest`'s role. Both now
exist:

- `coop_ids.go` — every real Salmon Run and Big Run stage id, every real
  weapon id (including the Grizzco/kuma pool), and the three real king
  salmonid boss codenames, all read directly out of the game's own shipped
  parameter tables (see that file's package comment for exactly which ones
  and how they were cross-checked against each other and against Inkipedia's
  independently maintained Big Run stage list).
- `coop_schedule.go`'s `CoopShift` + `Validate` — the `SaveFest`-equivalent:
  an operator describes a shift (kind, stage, weapons, boss, timing) and it
  is checked against `coop_ids.go`'s real tables before being stored, the
  same "explain the constraint in Go, not in a column" split `Fest.Validate`
  uses.
- `switchnetctl coop schedule` (and `list`/`delete`/`stages`/`weapons`/
  `bosses` to print the reference tables) — the CLI surface, mirroring
  `switchnetctl fest build`.
- `internal/dashboard`'s "Salmon Run" tab — the same `CoopShift`/`Validate`
  path behind a form, mirroring the "Splatfests" tab: a Kind toggle filters
  the stage dropdown to Big Run or normal-rotation stages (a real console
  never sees the two combined), the weapon/kuma/boss dropdowns are the exact
  tables `switchnetctl coop weapons`/`bosses` print, and the scheduled list
  shows each shift's live/upcoming/ended status. See
  `internal/dashboard/coop.go`.

TeamContest (Eggstra Work) has no authoring path yet; adding one is the same
shape of work Big Run was. Three fields on the wire message are an
operator-local assumption rather than a confirmed shape — `coopScheduleProto`
documents which, and `docs/QUESTIONS.md` has the entry.

## `CloudSave.GetSaveRecord`: `NotFound` is the correct answer, not a stub

A cloud save is real player save state. SwitchNet has no legitimate way to
have one — the same "no game dump, no save data" boundary CLAUDE.md draws
for phase 3 applies here too. `GetSaveRecord` answers `NotFound` for every
account, which is not a placeholder: a fresh account has no cloud save on a
real server either, and the game already has to handle that case on its
very first boot.

The other five `CloudSave` RPCs are left to
`UnimplementedCloudSaveServer`. Nothing calls them this early, and actually
implementing `Write`/`Create`/`Delete` convincingly would mean this server
keeping opaque save blobs it has no way to validate — a materially
different scope than answering the one boot query that would otherwise
hang.
