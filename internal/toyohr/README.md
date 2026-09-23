# internal/toyohr

Splatoon 3's own NPLN services (`nn.npln.toyohr.v1.*`). Splatfests first,
because custom Splatfests are what phase 2 exists for.

## Every service is registered, including the empty ones

`Register` attaches all ten, even the six with no handlers. That is deliberate:
an RPC on a registered service answers `Unimplemented` and `internal/npln`'s
interceptor **logs it once, naming the method**, whereas an RPC on a service
that was never registered fails the same way with nothing in the log. ROADMAP
§2.4 says to let the console drive the order of this backlog, and that only
works if the console's asking is recorded.

`backlog.go` carries a note on each unimplemented one saying what taking it on
would involve. `GameRecord` is the one that matters most — battle results come
from there, and battle results are the missing half of a fest verdict. Its
`InitializeAttributes` is implemented (`game_record.go`): it creates the
player's `VsUserAttribute`/`CoopUserAttribute` document in `internal/ugcstore`
from only what the request carries, since the game keeps its own defaults for
any key that is absent.

| | |
| --- | --- |
| Reimplements | `nn.npln.toyohr.v1.FestService`, `Schedule`, `LobbyMessaging`, `UserScreening`, `CloudSave.GetSaveRecord` |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Definitions | [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols) `latest/proto/toyohr/v1/{fest,schedule,cloud_save,messaging}.proto` |
| Needs a database | yes, for fests and coop shifts -- the rest of `Schedule` and `GetSaveRecord` need none |

## The package name is confirmed, not guessed

A 2026-09-20 decompile of Splatoon 3 v11.3.0's own `main` (see
`internal/npln/README.md`'s "confirmed RPC surface" section for how it was
extracted) settles something this package's naming had been assuming:
Nintendo's own build embeds the source path
`bazel-out/arm64-v8a-opt/bin/proto/toyohr/v1/fest.pb.cc`, and every
`FestService` RPC appears as a literal
`/nn.npln.toyohr.v1.FestService/<Method>` path string. `toyohr` is Nintendo's
real internal package name, not a guess carried over from `kinnay/NPLN-Protocols`.

The same decompile gives the exhaustive RPC list this specific game links
against (6 real RPCs, 5 implemented):

| RPC | Implemented |
| --- | --- |
| `CreateFestEntry` | yes |
| `GetFestEntry` | yes |
| `GetFestResult` | yes |
| `SelectFestSchedule` | yes |
| `GetFestDecryptionKey` | yes |
| `SelectFestBreakingNews` | **no** -- not registered as a handler; falls through the same `Unimplemented` path as the backlog in `backlog.go` |

`SelectFestBreakingNews` reads, from the name alone, like mid-fest
announcement content -- lower stakes than any of the six already implemented,
and its request/response shape is undocumented, so it stays unimplemented
rather than guessed at. Add it to `backlog.go` alongside `GameRecord` if the
console is ever seen calling it.

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
positionally — notice, start, result, then the three per-team keys. A misordered
key is handed to the console for the wrong phase and the pack fails to open, with
nothing saying which phase was wrong.

There are **five packs and six key fields**, so the mapping is named rather than
positional: `bcat.KeyFields` and `bcat.PackForKeyField`. Treating the pack list
as a key order put `wa`'s key in `result_winning_team` and `wb`'s in
`team_alpha`, and every team pack failed to open.

## Six key slots, not three

The message declares six key fields and the request carries a `show_all_keys`
flag. A reference implementation this protocol knowledge was checked against
caps its response at three, on the grounds that the game never asks for more —
which is plausibly true of the phases it was tested against and is not a
property of the protocol. All six are filled when supplied.

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

## The schedule: the mechanics are ours, the numbers are not

`Schedule` generates the versus rotation rather than storing it: a rotation is a
deterministic function of the clock and the operator's stage list, so there is
nothing to keep in sync, and the same request at the same time always produces
the same answer — which is what makes the etag worth anything.

What this package decides: two-hour slot alignment (the same 7200-second bucket
a fest's phases must land on), a twelve-slot horizon, back-to-back windows with
no gaps, wrapping rather than running out, and an etag over exactly the values a
client would act on.

What it does **not** decide: the stage and rule identifiers. Those are Splatoon
3's own internal numbering, nothing public documents them, and an invalid one's
behaviour on the console is itself unknown. They come from `npln.rotation`, as
strings so a typo is an error rather than quietly becoming stage 0 — which is a
plausible valid id, so a parse failure would be indistinguishable from an
intention.

An empty rotation answers with no schedules, which is what the game sees between
seasons on a real server, and logs once per kind so the operator can tell that
from a broken server.

`SelectCoopSchedules` is **not** empty: every operator-scheduled Salmon Run
shift that has not fully ended yet is served for real, soonest first. Unlike
the versus rotation, `nn.npln.toyohr.v1.CoopSchedule`'s own decompiled proto
already carries the real shape, so once `coop_ids.go` supplied real
stage/boss/weapon/reward ids and an authoring path (`SaveCoopShift`,
`GenerateAndSaveCoopShifts`, `switchnetctl coop`), there was nothing left
that genuinely needed a capture — see `coop_schedule.go`.

## Lobby messaging: a message, not a mailbox

The hub is in memory, and that is the design rather than a shortcut. A lobby
message is worth nothing to a player who was not connected when it was sent —
persisting them would mean deciding when they expire, and the answer is
"immediately".

Three properties:

- **the sender comes from the token.** `SendMessage` carries a `user` field;
  honouring it would be impersonation with extra steps.
- **the lobby is an opaque topic string.** Which lobbies exist is the game's
  business, and treating it as a string rather than modelling it means there is
  nothing here to get out of step with the game.
- **a send that would block is dropped**, and a message nobody received is not
  an error. A console that stopped reading must not stall delivery for everyone
  else, and a message to a player who just quit is a message nobody gets —
  reporting that as a failure has the sender retry something that cannot
  succeed.

A lobby message is not echoed to its sender: their console already knows what it
sent, and echoing shows the player their own message twice.

## Reports are logged, not filed

`CreateReport` accepts and logs at **warn**. Accepting because the console shows
a failure here to the player, and somebody reporting another player should not
see a communication error. Logging rather than storing because this is a private
network of consoles one person owns: there is no moderation queue, and the useful
outcome of a report is that the operator hears about it.

`GetViolation` always reports none. SwitchNet issues no penalties — there is
nobody to issue them, and a private server that silently banned one of the
operator's own consoles would be a bug with no way to see it.

## The vote is measured; the battle ratios are not

`GetFestResult` fills four sections, and they are not equally real.

**Yobisai — the pre-fest vote — is measured.** Every `CreateFestEntry` is a
player who picked a side, and the ratio of entries per team is a number this
server actually has.

**The Honsai midterm and final ratios are not.** A real fest result carries
per-category ratios derived from battles played, and nothing in SwitchNet
records a battle yet — `GameRecord` is unimplemented. Those are returned with
`is_valid` false, which is the protocol's own way of saying a section is
unavailable.

They are **not** filled with a plausible number, and that is the point: a
fabricated ratio is indistinguishable from a measured one to whoever reads it
next, including a later version of this code.

**So the verdict is decided on the vote.** One point per entry, most points
wins. That is an *editorial choice*, not a stand-in — this is one operator's
server, a fest here is a fest SwitchNet built, and "the side more people picked
wins" is a rule that can be stated to the players. `overallResult` is the single
function that changes when battles start being recorded.

The verdict is withheld until play is over. Publishing one mid-fest would have
the game announce a winner while the fest is still open.

## Team choice locks when play opens

Before a fest starts, calling `CreateFestEntry` again changes the team — that is
a player changing their mind in the lobby. Once play opens:

- **a player who already picked gets their existing entry back**, not an error.
  A console re-sending its choice on reconnect is normal traffic, and an error
  there reads to the player as the fest being broken.
- **a player who never picked is refused.** Splatoon 3 makes players choose
  before a fest opens, and a vote arriving after a ratio was published changes a
  number the players have already seen.

A fest that is disabled, or not yet announced, does not exist to a console at
all — `NotFound`, not a more specific refusal.

## The player comes from the token, never from the request

`FestEntry` carries fields the client fills in. The player is taken from the
authenticated session instead, through the `CallerID` function
`internal/npln` hands in — it cannot be an import, because `npln` registers this
service and the cycle would not build.

`CallerID` nil is legitimate and **fail-closed**: the dashboard builds a
`Service` to schedule fests and never serves an RPC through it, so a
player-scoped handler reached that way refuses rather than guessing. Guessing is
the bug this repository is shaped around.

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
(`stage`, `main_weapons`, `kuma_weapon`) plus what only some of them carry:
`boss` and `reward_gear_id` on `Normal`/`BigRun` but not `TeamContest`,
`rewards` on `BigRun`/`TeamContest` but not `Normal`. Independently
corroborated, not just read once:
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
- `coop_generate.go`'s `GenerateCoopShifts` — fills a stretch of the normal
  rotation with randomly-picked real ids rather than requiring every shift
  to be hand-typed, for an operator who wants Salmon Run to keep changing
  without babysitting it. **Not a recovered Nintendo algorithm** -- no
  public source documents how the real server actually picks a rotation,
  only that it does, on a real, cited cadence (`DefaultCoopShiftDuration`'s
  comment: 40 hours, confirmed against two independent public sources,
  distinct from Big Run's own 48-hour window). The one thing this generator
  guarantees beyond "pick something real" -- avoiding an immediate stage
  repeat between consecutive shifts -- is this project's own design choice,
  not a discovered rule. `Service.GenerateAndSaveCoopShifts` is additive: it
  never touches a slot that already holds a shift, hand-scheduled or
  previously generated, which is what makes "mostly automatic, but a
  specific slot can always be corrected by hand" true rather than aspirational
  -- `switchnetctl coop generate` and the dashboard's "Auto-fill" panel are
  both thin wrappers over it.

TeamContest (Eggstra Work) has an authoring path now too, the same shape of
work Big Run was: `CoopShiftTeamContest`, validated against the normal
rotation's stages (not Big Run's) and, unlike Normal/BigRun, refusing a boss
or a reward gear id — `CoopSchedule_TeamContest` has no field for either, so
a value there is an operator mistake to explain rather than a value
`coopScheduleProto` would silently drop. No generation path, deliberately,
the same reasoning `coop_generate.go` already gives for excluding Big Run:
Eggstra Work is a calendar event with a small, real history to hand-schedule
from, not a rotation to synthesize entries for. Three fields on the wire
message are an operator-local assumption rather than a confirmed shape —
`coopScheduleProto` documents which, and `docs/QUESTIONS.md` has the entry.

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
