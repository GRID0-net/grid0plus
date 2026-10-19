# toyohr

Splatoon 3's own NPLN services (`nn.npln.toyohr.v1.*`): Splatfests, the stage and Salmon Run schedules, per-player cloud save, lobby
messaging, the plaza and share codes.

| | |
| --- | --- |
| Stands in for | `nn.npln.toyohr.v1.FestService`, `Schedule`, `LobbyMessaging`, `UserScreening`, `CloudSave`, `GameRecord`, `Locker`, `Canola`, `Replay` and `CoopScenario` |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Definitions | [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols), `latest/proto/toyohr/v1/`, plus `scripts/proto-patches/grid0plus.patch` |
| Needs a database | yes, for fests and Salmon Run shifts. The rest of `Schedule` and `GetSaveRecord` need none. |

## Every service is registered, including the empty ones

`Register` attaches all ten, even the ones with no handlers. An RPC on a registered service answers `Unimplemented` and the interceptor in
[`internal/npln`](../npln/README.md) logs it once, naming the method. An RPC on a service that was never registered fails the same way with
nothing in the log. That log is how you find out what the game asks for that the server can't answer yet. `backlog.go` notes what each
unimplemented one would involve.

## FestService

The game links six RPCs: `CreateFestEntry`, `GetFestEntry`, `GetFestResult`, `SelectFestSchedule`, `GetFestDecryptionKey` and
`SelectFestBreakingNews`. All are implemented. `SelectFestBreakingNews` isn't in the proto; `fest_breaking_news.go` adds it to the service
descriptor and always answers the empty message, because an `Unimplemented` there is a hard error in game.

`GetFestResult` is addressed under the separate `festResults/<id>` collection, not `fests/<id>`, and echoes the requested name. Answering that
normal path with `InvalidArgument` stops the hall flow before the console can continue to the BCAT download.

A fest that is disabled, or not yet announced, doesn't exist to a console at all: `NotFound`, not a more specific refusal. With no active fest,
`SelectFestSchedule` answers an empty message.

### A Splatfest lives in two places, and they must agree

```
NPLN schedule  (this package)    names a fest: its id, its pack, its revision
BCAT pack      (internal/bcat)   carries the CONTENT, and names the fest it is for
```

`SelectFestSchedule` carries no content at all (no team names, banners or idol dialogue): only which pack, which revision of it, the five phase
times and the publication window. Everything a player sees is inside the BCAT pack the console fetches separately. So the announced fest id,
resource name and revision must match what the console's BCAT cache holds. When they don't, the game asks BCAT for a resource digest it can't
find and reports `BcatInvalid`, which reaches the player as a generic communication error.

### Three details that each cost somebody an evening

- **Timestamps must land on two-hour boundaries.** Splatoon 3 files scheduled things into 7200-second slots. A fest whose phases aren't aligned
  belongs to no slot, so the game stops treating it as open and demands its verdict, which a real console never does mid-fest. Every player's
  lobby fails at once with nothing in any log. Every phase offset is a multiple of the slot length, so aligning the start aligns the whole table.
  `NewFest` aligns, and `Validate` refuses: a caller asking for "next Saturday" wants a fest, but an operator who typed a time should be told it
  moved rather than have it change quietly.
- **The fest id is uppercase and the resource name is lowercase**, and they fail differently. A lowercase id is a different fest to the game. A
  capitalised resource name is worse, because the console derives the pack's AES IV from that string, so one wrong capital means the pack never
  opens and nothing in the failure points back at the name.
- **A key in the wrong slot is silent.** `GetFestDecryptionKey` returns keys by position: notice, start, result, then the three per-team keys. A
  misordered key reaches the console for the wrong phase and the pack fails to open with nothing saying which phase was wrong. There are five packs
  and six key fields, so the mapping is named (`bcat.KeyFields`, `bcat.PackForKeyField`) rather than positional.

All six key slots are filled when supplied (the request carries a `show_all_keys` flag). A key that wasn't supplied is left absent rather than empty,
because Nintendo omits the key for a phase that hasn't happened yet and an empty string is a key the console would try to use. Ordinary requests get
the notice and start keys during the announcement and playable phases. The stored team keys appear only after results, on an explicit
`show_all_keys` request, and the result key and winner stay withheld until the results phase. After results open, the winning team's `wa`, `wb` or `wc`
archive key is chosen with the same calculation `GetFestResult` uses.

**The keys are GRID0+'s own.** Nothing here decrypts anything of Nintendo's. A fest GRID0+ serves is one GRID0+ built, with packs encrypted under keys
stored alongside it, and `GetFestDecryptionKey` hands back those same keys. That is what makes a genuinely custom Splatfest possible.

### Scoring

`GetFestResult` fills four sections.

- **Yobisai is the Conch Shells share, and is sent as 0 for every team.** The game shows it as the Conch Shells share, and GRID0+ counts no shells: LAN players never reach it. **The vote is measured**: every `CreateFestEntry` is a player who picked a side, and the ratio of entries per team is what the vote ranks and what an empty midpoint falls back to.
- **Completed gamesync referee reports are recorded once per battle.** The verified session supplies the match and player identities, and each player's
  team comes from their stored fest entry. GRID0+ counts wins by mode and sums reported paint points by team. The midpoint freezes at its scheduled time and
  uses pre-midpoint paint points, then wins, then the vote if no reports exist.
- **At results time**, the ranking weights are 70, 35 and 0 for popularity (the vote), 120, 60 and 0 each for open and challenge battles, and 180, 90 and 0 for tricolor. Yobisai's 90, 45 and 0 is sent as 0, 0 and 0, because nobody's shells can be counted. A battle category with no recorded wins gives nobody points and shows every team at 0%. Wins, not Nintendo's undisclosed formula, rank each battle category. Tied teams share the best rank of their tie group, except at 0%, where a team with nothing in a category scores nothing there.

`GetFestResult` and the result-pack winner in `GetFestDecryptionKey` read the same scoring function, so the announced winner matches the points the game shows.
The verdict is withheld until play is over, because publishing one mid-fest would have the game announce a winner while the fest is still open.

### The first pick is final

Splatoon 3 never lets a player change team once they have picked one, and neither does GRID0+. The first entry stands whoever made it: this console, another console on the same account, or the Discord bot's vote post (which writes the same `npln_fest_entries` row). `Store.CreateFestEntry` inserts only when there is no entry, so a console and the bot voting at the same instant cannot both win.

- A player who already picked gets their existing entry back, not an error, in every phase, because a console re-sending its choice on reconnect is normal traffic.
- A console confirming a bot-picked team fills in the region, which the bot cannot know.
- A player who never picked can still pick until play ends, because Splatoon 3 asks a player with no entry to choose when they open the game mid-fest and refusing that is a communication error. The late vote does move the vote ratio. After play ends a new entry is refused.
- Each new entry is handed to `Options.OnEntry`, which `internal/npln` publishes for the Discord bot, so a player who picks in game gets the team's role on the server at once.

### The player comes from the token

`FestEntry` carries fields the client fills in. The player is taken from the authenticated session instead, through the `CallerID` function `internal/npln` hands in
(an import would be a cycle). A nil `CallerID` is legitimate and fails closed: the dashboard builds a `Service` to schedule fests and never serves an RPC through it,
so a player-scoped handler reached that way refuses rather than guessing.

Two fests live at once is a state the game has no reading for. The operator can create it by mistake, so `currentFest` picks the most recently started and logs a
warning.

## Schedule

`SelectVsSchedules` and `SelectSeasonSchedules` are generated live from the clock and the operator's stage and rule lists (`rotation.*` in the config, with the real
ids in `vs_ids.go`): twelve two-hour slots starting with the previous one, one `schedule_set_id` for the window, Bankara, X and League settings on each slot, and
quoted 24-character ETags. A rotation is a deterministic function of the clock and the stage list, so there is nothing to keep in sync, and the same request at the
same time always gets the same answer, which is what makes the ETag meaningful. `SelectSeasonSchedules` carries the season id and a dedicated `target` field
(added in `scripts/proto-patches/grid0plus.patch`, since upstream's proto stops at field 7). `SelectLeagueSchedules` answers empty, since GRID0+ has no authored
League events.

What the package decides is the two-hour slot alignment, a twelve-slot horizon, back-to-back windows with no gaps, wrapping instead of running out, and an ETag over
exactly the values a client acts on. What it doesn't decide is the stage and rule ids, which are Splatoon 3's own numbering and come from `npln.rotation` as strings,
so a typo is an error rather than quietly becoming stage 0, which is a valid id. An empty rotation answers with no schedules, as the game sees between seasons on a real
server, and logs once per kind so you can tell that from a broken server.

`SelectVsParams` answers a real, non-nil `VsParams` timestamped off the current clock, with the eleven tuning attributes (`vsParamsAttributes` in `schedule.go`), a
derived non-empty ETag, and a window spanning the generated rotation horizon. Left unset, the client stops advancing the rotation, and the console goes quiet before it
calls `PresenceService/SubscribePresences`.

**Resource names must have the shape the game's parser expects, or it aborts.** `VsParams.Name` is `tenants/<t>/targets/<target>/vsParams/<rest>` and
`CoopSchedule.Name` is `tenants/<t>/targets/<target>/coopSchedules/<rest>`. A name that doesn't parse that way makes the game hard-abort. For `VsParams`, `<target>`
is the last path segment of the request's own `Target` (`default`), not the whole alias path. For `CoopSchedule` it carries the shift's kind.

### Salmon Run

`SelectCoopSchedules` serves every operator-scheduled shift that hasn't fully ended, soonest first. `CoopSchedule` has three parallel sub-messages: `Normal`,
`BigRun` and `TeamContest` (Eggstra Work). The content and authoring path are:

- `coop_ids.go`: every Salmon Run and Big Run stage id, every weapon id (including the Grizzco pool) and the three king salmonid boss codenames, from the game's
  own public parameter tables.
- `coop_schedule.go`: `CoopShift` and `Validate`. An operator describes a shift (kind, stage, weapons, boss, timing) and it is checked against `coop_ids.go` before
  it is stored. Eggstra Work is validated against the normal rotation's stages and refuses a boss or a reward gear id, since its message has no field for either.
- `switchnetctl coop schedule`, with `list`, `delete`, `stages`, `weapons` and `bosses` to print the reference tables.
- the dashboard's Salmon Run tab, which uses the same path behind a form.
- `coop_generate.go`: `GenerateCoopShifts` fills a stretch of the normal rotation with randomly picked real ids, for an operator who wants Salmon Run to keep changing
  without babysitting it. It isn't Nintendo's algorithm. It does avoid an immediate stage repeat between consecutive shifts. `Service.GenerateAndSaveCoopShifts` is
  additive: it never touches a slot that already holds a shift, so a specific slot can always be corrected by hand. Big Run and Eggstra Work are calendar events and
  aren't generated.

Three fields of the Eggstra Work message are an operator-local assumption rather than a confirmed shape; `coopScheduleProto` documents which.

## LobbyMessaging

The hub is in memory, which is the design and not a shortcut: a lobby message is worth nothing to a player who wasn't connected when it was sent. The sender comes from
the token, because the `user` field in `SendMessage` would be impersonation with extra steps. The lobby is an opaque topic string, so there is nothing to get out of step
with the game. A send that would block is dropped, and a message nobody received isn't an error, because a console that stopped reading mustn't stall delivery for
everyone else. A lobby message isn't echoed to its sender. `RecvMessage` sends its idle keep-alive every 20 seconds.

## UserScreening

`CreateReport` accepts and logs at warn level. It accepts because the console shows a failure to the player, and somebody reporting another player shouldn't see a
communication error. It logs instead of storing because there is no moderation queue on a private network: the useful outcome of a report is that the operator hears of it.

`GetViolation` always reports none, because GRID0+ issues no penalties. **An absent violation must be `NotFound`, with response headers before the status
trailers.** A named successful response means a violation exists and can block a fresh player from the hall, and a successful response with no message decodes to an
empty resource name that can crash the game's resource-path parser. A gRPC test checks both the status and the opening `npln-grpc-type` header.

## CloudSave

The console keeps part of its progression in a server-side save record, and the hall depends on it. `save_store.go` stores it per player in the Ugcstore document table under
`tenants/<t>/saveRecords/<uid>`, outside the `documents/` prefix so Ugcstore never serves it.

- `GetSaveRecord` returns the stored record, or zero messages when there is none (a hand-rolled stream, since a unary handler can't send nothing). `NotFound` here loops the
  console on a communication error, and a zero-message answer makes a fresh console upload its own save.
- `WriteSaveRecord` merges the delta the way Nintendo's server does: maps merge recursively, null deletes, an empty array is stored as null and anything else replaces. It
  returns a strictly increasing `update_time`, which the console presents as the next write's version token.
- `CreateSaveRecord` stores a new console's full record.
- `ValidateSaveRecord` acknowledges.

Every value comes from the player's own console, or from an imported save (see [DISCORD_BOT.md](../../docs/DISCORD_BOT.md)). GRID0+ never invents one. `ChangeUserName` and
`DeleteSaveRecord` stay `Unimplemented`.

## GameRecord

`InitializeAttributes` creates the player's `VsUserAttribute` or `CoopUserAttribute` document in [`internal/ugcstore`](../ugcstore/README.md) from only what the request carries,
because the game keeps its own defaults for any key that is absent. It accepts `tenants/current/users/current` as the already authenticated caller and stores the document under
that caller's real user id, never under the literal alias.

`PrepareTag`, `PrepareBankaraPowerMeasurement`, `PrepareLeaguePowerMeasurement`, `PrepareWeaponPowerMeasurement` and `ResetUdemae` are commented out as unknown in
NPLN-Protocols. Their field numbers and types are added in `scripts/proto-patches/grid0plus.patch`. The handlers (`game_record_prepare.go`) answer the measurements with the
caller's own `GameRecord/users/<uid>/<Kind>PowerMeasurement/<id>` document, `PrepareTag` with an empty tag and `ResetUdemae` with an empty response, and log every request.

## Locker, Canola, Replay and CoopScenario: the plaza and share codes

All in `backlog.go`. The posts themselves are Ugcstore documents and attachments; these services only list, register and resolve them.

| RPC | What GRID0+ does |
| --- | --- |
| `Locker` and `Canola` `SelectDocuments` | the plaza: each player's newest published post, newest first, 31 lockers and 6 decks. A request naming players (`Normal3`, `UIDsInOrder`) gets those players' newest posts instead, which are the name plates beside their names. |
| `Canola` `SelectDocuments`, type `Fest` | the fest plaza: drawings posted during that `FestID`, unhidden, newest per player |
| `Locker` and `Canola` `RegisterDocument` | acknowledged with the empty message Nintendo answers |
| `Locker` and `Canola` `IkasuDocument` | a "fresh!" (like): acknowledged for a post in that service's plaza collection, `NotFound` otherwise. Nothing is kept. |
| `CoopScenario` `RegisterDocument` and `ResolveCoopScenarioCode` | a Salmon Run job shared as a code under `grid0/coopScenarioCodes` |
| `Replay` `RegisterDocument` and `ResolveReplayCode` | a replay shared as an `R` code under `grid0/replayCodes`, resolved through the same path |

A code resolves only to a document of its own kind: a scenario code typed into the replay screen is `NotFound`.
