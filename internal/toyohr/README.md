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
| Reimplements | `nn.npln.toyohr.v1.FestService`, `Schedule`, `LobbyMessaging`, `UserScreening`, `CloudSave.{GetSaveRecord,WriteSaveRecord}` |
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
| `SelectFestBreakingNews` | yes -- always an empty message, see `fest_breaking_news.go` |

`SelectFestBreakingNews` is not in the proto; `fest_breaking_news.go` adds
it to the service descriptor and answers the empty message Nintendo answered
during a real Splatfest. An `Unimplemented` there is a hard error in-game.

`GetFestResult` is addressed under the separate `festResults/<id>` resource
collection, not `fests/<id>`. The result response echoes that requested name;
returning `InvalidArgument` for this normal path stops the hall flow before the
console can continue to the BCAT download.

After results open, `GetFestDecryptionKey` uses the same calculated winner as
`GetFestResult` to select the `wa`/`wb`/`wc` archive key for response field 4.
A stored explicit result key takes precedence for older single-outcome seeds.
Ordinary calls omit the optional per-team keys, including in the results phase.

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

Nothing here decrypts anything of Nintendo's. A fest GRID0+ serves is a fest
GRID0+ built: its pack was encrypted with keys stored alongside it, and
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

`GetViolation` always reports none. GRID0+ issues no penalties — there is
nobody to issue them, and a private server that silently banned one of the
operator's own consoles would be a bug with no way to see it.

**An absent violation must be NotFound with response headers before the
status trailers.** A named successful response means a violation exists and
can block a fresh player from the hall. A successful response without a
message decodes to an empty resource name and can crash Splatoon 3's
resource-path parser, as observed on GRID0+ hardware. The working
Nextendo implementation uses a two-frame NotFound for a player without a
violation. GRID0+ now does the same, and a gRPC test checks both the
status and the opening `npln-grpc-type` header. The upstream proto retains
the `name` field for a real violation if this server ever supports one.

## Fest scoring

`GetFestResult` fills four sections, and they are not equally real.

**Yobisai — the pre-fest vote — is measured.** Every `CreateFestEntry` is a
player who picked a side, and the ratio of entries per team is a number this
server actually has.

**Completed gamesync referee reports are now recorded once per battle.** The
verified session supplies the match and player identities; each player's team
comes from their stored fest entry. GRID0+ counts wins by mode and sums reported
paint points by team. The midpoint freezes at its scheduled time and uses
pre-midpoint paint points, then wins, then the vote if no reports exist.

At results time, the five captured ranking weights are 90/45/0 for the vote,
70/35/0 for popularity, 120/60/0 each for open and challenge battles, and
180/90/0 for tricolor. A battle category with no recorded wins uses the vote
ranking; this is explicitly an editorial fallback, not a measured battle
result or a claim to reproduce Nintendo's undisclosed clout formula. Tied
teams share the last rank occupied by their tie group.

`GetFestResult` and the result-pack winner in `GetFestDecryptionKey` read the
same scoring function, so the announced winning team matches the points the
game displays. The result key and winner are withheld until results open.

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

## `Schedule`: generated rotations, shaped like the working captures

`SelectVsSchedules` and `SelectSeasonSchedules` are generated live from the
clock and the operator's stage/rule lists (`rotation.*` in `config.yaml`,
real ids in `vs_ids.go`): twelve two-hour slots starting with the previous
one, one `schedule_set_id` shared by the window, Bankara/X/League settings on
each slot, and quoted 24-character ETags — all as in both working Nextendo
hall captures. `SelectSeasonSchedules` carries season id 16 and the
`LastSeasonLeaderboardFixTime` attribute those captures show.
`SelectLeagueSchedules` answers empty: League events are authored content
and GRID0+ has none (`league_diagnostic.go` can serve a disposable real
one for testing).

**`SelectVsParams` used to be the fourth empty one, and that was wrong.**
Unlike an empty schedule list, `params` left unset is not what a real server
sends: NextendoNetwork/splatoon-3 measured (facts only, not its code) that it
replays a real captured `VsParams` payload rather than answering empty, and
separately that the payload's own two timestamps must ride the same clock
shift as everything else, or the client "cannot tie the current rotation to
its parameters" and stops advancing. Reproduced on GRID0+'s own hardware
on 2026-09-24: the console called this RPC successfully and then went silent
before ever calling `PresenceService/SubscribePresences`, the next call in
the confirmed reference sequence — the same crash family as
`GetSaveRecord`'s real `SaveRecord`.

`SelectVsParams` now answers a real, non-nil `VsParams`, timestamped off the
current clock so it is never stale, with the eleven tuning attributes the
working captures carry (`vsParamsAttributes` in `schedule.go`). Both working
captures return a nonempty, quoted ETag even when the request has none;
GRID0+ derives one from its own target and parameter-set id instead of
echoing the request. The captured timestamps also enclose the client's
current time; GRID0+ used to set both to the same instant, an empty
window. It now spans the generated rotation horizon. See
`docs/CAPTURES/npln-vsparams-etag-2026-09-24.md` and
`docs/CAPTURES/npln-vsparams-window-2026-09-24.md`.

**`VsParams.Name` needed a `targets/<target>` segment too — settled by
decompile, and now fixed.** The fix above still crashed real hardware, just
one call later. A 2026-09-24 decompile of Splatoon 3 v11.3.0's own
`main.nso` (this project's own hardware dump) found the client's
resource-name parser for a `VsParams` requires the same literal shape
`CoopSchedule.Name` does, below: `tenants/<t>/targets/<target>/vsParams/<rest>`.
The fix's own first answer, `tenants/<t>/vsParams/<id>`, omitted the
`targets/<target>` segment and reproduced the crash on real hardware.
Unlike `CoopSchedule`, `<target>` here is not a GRID0+ placeholder:
`SelectVsParamsRequest` already carries a `Target` field the client itself
sends.

**That fix's own first answer still crashed — `Target` is a full alias
path, not a bare id.** Echoing `Target` back whole was still wrong: a
VM-side loopback capture caught its real value, `tenants/current/targets/default`
— the same `tenants/current/...` alias convention
`GameRecord/InitializeAttributes` and `Friends/ActivateUser` already
resolve elsewhere in this package, not a bare id. Echoing it whole built
`tenants/<t>/targets/tenants/current/targets/default/vsParams/<id>` — two
nested `targets` segments — which reproduced the identical crash address a
third time, even after the segment-shape fix above had already shipped.
Only `Target`'s own last path segment (`default`) belongs in the response.

**`CoopSchedule.Name` needs a `targets/<target>` segment it did not have —
settled by decompile, and now fixed.** A 2026-09-24 decompile of Splatoon 3
v11.3.0 (this project's own console, real hardware) found the client's
resource-name parser for a `CoopSchedule` requires the literal shape
`tenants/<t>/targets/<target>/coopSchedules/<rest>` — checked with a hard
`memcmp` against `"targets"` and `"coopSchedules"` as the third and fifth
path segments — and hard-aborts (User Break, `nn.npln.Worker`) when a name
does not parse that way. `coopScheduleProto`'s old name,
`tenants/<t>/coopSchedules/<id>`, omitted the `targets/<target>` segment
entirely and reproduced this crash on real hardware with four real coop
shifts seeded — the doc comment there used to claim a wrong Name only "costs
a cosmetic mismatch, not a broken RPC", which this decompile disproves.
`<target>` is opaque to the parser — extracted and stored, never compared
against a literal — so it now carries the shift's own `Kind`
(`"normal"`/`"big_run"`/`"team_contest"`), a stable GRID0+ placeholder,
not reconstructed Nintendo data (`TODO(capture-needed)`, in
`coop_schedule.go`).

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

## `SeasonSchedule.target`: a field missing from upstream's own reconstruction

Both preserved working hall captures carry an eighth field on every
`SeasonSchedule` entry that decodes to the raw string `"default"` -- absent
from kinnay/NPLN-Protocols' own `resources.proto` (it stops at field 7,
`schedule_set_id`) as well as from what GRID0+ had been sending. This is
the same target concept `VsSchedule`/`LeagueSchedule` already embed in their
own resource names' `targets/default` segment, just carried as a dedicated
field here instead. Added as `string target = 8` in
`scripts/proto-patches/grid0plus.patch`, which `scripts/fetch-proto.sh`
reapplies on every fetch, and `SelectSeasonSchedules`
now sets it from the request's own `Target` field via the same
`scheduleTarget` helper `SelectVsSchedules`/`SelectCoopSchedules` already
use, rather than a bare hardcoded `"default"`.

## GameRecord's `Prepare*` and `ResetUdemae`: shapes from the game's parsers

`PrepareTag`, `PrepareBankaraPowerMeasurement`,
`PrepareLeaguePowerMeasurement`, `PrepareWeaponPowerMeasurement` and
`ResetUdemae` are commented out as unknown in NPLN-Protocols. Their field
numbers and types were recovered from Splatoon 3's own generated parsers and
added in `scripts/proto-patches/grid0plus.patch`; the names are ours. How,
and the table of shapes: `docs/CAPTURES/README.md`, "GameRecord calls
recovered from the game's parsers". The handlers
(`game_record_prepare.go`) answer the measurements with the caller's own
`GameRecord/users/<uid>/<Kind>PowerMeasurement/<id>` document, PrepareTag
with an empty tag and ResetUdemae with an empty response, and log every
request -- none has been seen from a console yet.

## `CloudSave`: each player's save record, stored

The console keeps part of its progression in a server-side save record, and
the hall depends on it. NextendoNetwork/splatoon-3 measured (facts only, not
its code) three answers for a player with no record: `NotFound` loops on a
communication error; a zero-message success makes a fresh console upload its
own save (`CreateSaveRecord`) and an established one send `evt=Validate`
writes; only a populated record gets the game on to `FestSchedule` and its
GameRecord document reads.

So `save_store.go` persists it, per player, in the Ugcstore document table
under `tenants/<t>/saveRecords/<uid>` (outside the `documents/` prefix, so
Ugcstore never serves it):

- `GetSaveRecord` returns the stored record, or zero messages when there is
  none (a hand-rolled stream, since a unary handler cannot send nothing);
- `WriteSaveRecord` merges the delta the way Nintendo's server does — maps
  merge recursively, null deletes, an empty array is stored as null,
  anything else replaces — and returns a strictly increasing `update_time`,
  which the console presents as the next write's version token;
- `CreateSaveRecord` stores a new console's full record;
- `ValidateSaveRecord` acknowledges.

Every value comes from the player's own console; GRID0+ never seeds one.
`ChangeUserName` and `DeleteSaveRecord` stay `Unimplemented`.

`GameRecord/InitializeAttributes` accepts `tenants/current/users/current` as
the already authenticated caller and stores the document under that caller's
actual user ID. It does not use the literal alias as a shared document owner.

## `SelectFestSchedule` without an active fest

Both preserved successful hall captures answer with an empty protobuf when no
fest is active. GRID0+ previously returned a synthetic expired fest instead.
A one-account hardware probe of the empty answer removed a BCAT 2122-1058
error on boot, although the hall still returned Splatoon's communication error.
The earlier empty-response crash occurred with an older set of responses; it
cannot be attributed to the no-fest answer alone. A configured fest still
receives its real schedule during its publication window.

`LobbyMessaging/RecvMessage` sends its idle keepalive every 20 seconds, as
measured in both successful 2026-09-23 hall captures; see
[the capture comparison](../../docs/CAPTURES/npln-stream-liveness-2026-09-24.md).

Ordinary fest-key requests publish notice and start keys during the announcement
and playable phases, matching Nextendo's active-fest capture. Stored team-key
fields are exposed only after results on an explicit `show_all_keys` request; a schema field
being available does not establish that the game expects it during startup.
The result key and winning team remain withheld until the results phase.
