# TODO

Work that is planned but not done, written for whoever picks it up next —
which is usually an AI agent with no memory of how any of this got here.

**Read [`CLAUDE.md`](CLAUDE.md) first.** Its ground rules are not optional and
several items below exist *because* of them. In particular: never commit a
Nintendo key, certificate, ticket or dumped file; never forge a Nintendo-issued
credential; identity resolution is fail-closed with no fallback; and when a
shape is genuinely undocumented, mark it `TODO(capture-needed):` rather than
inventing something Nintendo-shaped.

## How to use this file

Each item states **what**, **why it is not done**, **where to start**, and
**how you will know it works**. That last one matters most: much of this code
fails silently on a console and looks fine from the server, so "it compiles" is
never the bar.

Keep the categories. Add items to the category they belong to rather than the
bottom. When you finish something, delete the item rather than marking it done
— `git log` is the record of what happened.

**If you are working autonomously, you cannot ask anything.** Several items
below would benefit from an operator's answer. Do not stop for one: write the
question into [`docs/QUESTIONS.md`](docs/QUESTIONS.md) with the assumption you
proceeded on and the single function that changes if the answer differs, then
carry on. An assumption implemented and written down beats a question waited
on.

Priority labels are about *unblocking other work*, not importance:

- **P1** — something else is waiting on it
- **P2** — wanted, nothing blocked
- **P3** — worth doing eventually

---

## 1. NPLN protocol

### 1.3 The JSON front end: vermillion, penne, frontline — **P1 for hardware, built**

`internal/nplnrest` now serves this, on `npln.rest_listen` (`cmd/npln`, a
second listener alongside the gRPC gateway). The hosts were already
classified (`ServiceNPLNRest`) and the nginx generator already emitted
their block; both now point at a real backend instead of a comment.

Penne's two endpoints (`GET /v1/frontlines`, `POST /v1/login_tickets`) are
implemented against a real field-level spec
([kinnay's wiki](https://github.com/kinnay/NintendoClients/wiki/Penne-Servers)),
including the literal `persistent_connection_params_simple` constants.
Vermillion's two (`GET /v1/accounts/config`, `GET /v1/accounts/devices`)
answer honestly-empty JSON — the wiki names the endpoints but gives no
response field table for either, unlike Penne's. Every other Vermillion
endpoint (device replace, family, notify, nearness-checks) is left
unanswered (404) rather than guessed at.

**Still open, each recorded in `docs/QUESTIONS.md`:**

- Whether Splatoon 3's boot actually needs anything from vermillion at
  all — the wiki describes that host as the *virtual game card* system, a
  general Switch feature, not anything Splatoon-specific, which sits
  uneasily with `hosts.go`'s existing "NPLN device initialisation"
  classification.
- How a console obtains a Penne id/password in the first place —
  `god.hac.lp1.penne.srv.nintendo.net`'s provisioning endpoints
  (`/v1/immigrate`, `/v1/penne_ids`) are undocumented past their path, and
  `god` itself is not served by `internal/nplnrest` (still `ServiceStubs`).
- What a frontline connection actually carries once open. `serveFrontline`
  accepts it and holds it open — confirmed safe from a real error-code
  capture — but sends nothing further, because no source found this pass
  documents the wire framing.

Do **not** invent a plausible Nintendo internal for any of the three
without a capture to check it against.

### 1.4 The rest of `nn.npln.toyohr.v1.*` — **P2, mostly done**

`Schedule` is built (`internal/toyohr/schedule.go`): all five RPCs answer
"nothing scheduled" rather than `Unimplemented`, since the map/mode/weapon
rotation content itself is Nintendo's own game design data with no dump or
capture to draw from — see `docs/CAPTURES/README.md`. `CloudSave` is built
only as far as `GetSaveRecord` (answers `NotFound`, the correct answer for
an account with no cloud save, not a stub); its other five RPCs
(`Create`/`Validate`/`Write`SaveRecord, `ChangeUserName`, `DeleteSaveRecord`)
are not attempted — nothing calls them this early, and actually writing save
state is real player data this server has no legitimate way to have.

The rest is `internal/toyohr/backlog.go` and `lobby_messaging.go` now:

- **`LobbyMessaging`**: `RecvMessage`/`SendLobbyMessage` are real, backed by
  `internal/lobbymessaging`'s per-lobby broadcast topic (membership =
  "currently subscribed", the same shape `internal/presencebus` and
  `internal/matchmaking`'s ticket `Watch` already use, so nothing invents a
  roster this codebase has no other record of). `SendMessage` stays
  `Unimplemented`: it addresses specific users but carries no `Lobby`
  field, and routing it would mean guessing at session-tracking no capture
  confirms — see the `TODO(capture-needed)` on it.
- **`Canola`/`Locker`**: `SelectDocuments` answers empty and `IkasuDocument`
  answers `NotFound` — SwitchNet has no `internal/ugcstore`, so both are
  honest rather than guessed. `RegisterDocument` (the write) stays
  `Unimplemented` on both.
- **`CoopScenario`/`Replay`**: `Resolve*Code` answers `NotFound` (nothing
  was ever registered, since `RegisterDocument` is unimplemented on both).
  `Replay.FulfillReservations` answers an empty, valid list rather than an
  error — the `ListDeviceAccounts`-vs-`GetDeviceAccount` distinction.
- **`UserScreening`**: `GetViolation`/`SelectSnapshot` answer `NotFound` --
  no screening exists. `CreateReport` is the one write implemented for
  real: it is data the caller hands over, not data this server needs to
  have already had, so there is no undocumented shape to get wrong. It is
  logged for the operator, not acted on -- SwitchNet has no moderation
  system to act with.
- **`GameRecord`**: left entirely `Unimplemented`. `InitializeAttributes`
  alone carries per-mode X Power seed values -- deeply Nintendo-internal
  rating data with no defensible default, exactly what CLAUDE.md's first
  ground rule forbids inventing.

Treat what remains as a backlog and let the console drive the order: an
unimplemented RPC answers `codes.Unimplemented` and logs, and **that log is
the to-do list**.

### 1.5 `gamesync` — **P2, built**

`internal/gamesync` (RPC layer) and `internal/gamesyncstore` (the document
store) now implement all of `nn.npln.gamesync.v1.Gamesync` except
`CreateRound` -- `IssueToken`/`RefreshToken`, `GetDocument`/`ReadDocuments`/
`ListDocuments`/`QueryCollectionIds`, `WriteDocuments`/`LazyWriteDocuments`,
`BeginTransaction`/`CommitTransaction`/`RollbackTransaction`, and the
`KeepUserSession` watch stream.

**Where it's reached turned out to differ from this section's original plan.**
`gamesync.npln.nintendo.net` is classified `ServiceNPLN` in
`internal/nintendo/hosts.go`, grouped with the tenant gRPC host by SNI --
not routed to a separate port. Nothing in the nginx generator or
`docker-compose.yml` ever wired the `npln.gamesync_listen: ":7575"` setting
this section used to describe to anything, so it has been removed from
`config/config.go` rather than left as a key that looks configured and does
nothing (see CLAUDE.md's configuration rules). `internal/gamesync.Service` is
registered on `internal/npln`'s own `*grpc.Server` instead, the same way
`internal/toyohr`'s services are -- see `internal/gamesync`'s README for the
evidence and `docs/QUESTIONS.md` for what would change this if a capture ever
shows the "own port" reading was right.

**What's simplified, not guessed at** (see `internal/gamesyncstore`'s README
for the full reasoning): transactions are atomic but not isolated across
concurrent transactions; `FieldTransform`'s `LoadGlobalValue`/
`StoreGlobalValue`/`ClearGlobalValue` and `WriteDocuments`/
`CommitTransaction`'s `DefermentOperation` have no Firestore equivalent and
no public documentation, so they are accepted (logged, in the deferment
case) rather than rejecting the whole call over a shape nothing here can
interpret. `CreateRound` is left `Unimplemented`: its own `Round` message
has fields named `Unk1` through `Unk4` in this project's own proto
reversal, so there is nothing defensible to fill in without a capture.

---

## 2. Splatfests and BCAT

### 2.1 Asset conversion: images → game-ready textures — **P2, large**

**This is the rewrite the dashboard currently points at.**

Today `internal/bcat` takes assets that are *already* in the game's formats
(`.bfres`, `.byaml`, `.sarc.zs`), assembles them into a SARC archive and
encrypts it. That is the whole of what "custom assets" means right now: the
operator produces files in Switch Toolbox and points the builder at them.

What is wanted is uploading a **PNG of the splashart, the tee designs, the
banners** and having the server produce a loadable pack.

**Why it is not done:** it is a genuinely large piece of work, not a missing
function. It needs:

- **BFRES authoring** — the container Nintendo's textures live in.
- **Texture encoding** — BC1/BC3/BC7 compression, plus the exact format each
  slot expects.
- **Tegra swizzling** — the GPU's block-linear memory layout. Getting this
  wrong produces a texture that loads and renders as garbage, which is worse
  than one that fails.
- **BYAML editing** — team names and idol dialogue live in structured data
  inside the pack, not in the images.

**Strongly consider not writing this in Go.** Switch Toolbox and the
`BinaryTextureLibrary`/`Syroot.NintenTools` family already do it, and they exist
because it is hard. Shelling out to an existing converter, or vendoring one, is
likely to beat a reimplementation that is subtly wrong in ways only a console
reveals.

**Start at** `internal/bcat/build.go` (`BuildPacks`), which is where converted
files would enter, and the dashboard's Assets panel
(`internal/dashboard/templates/index.html`), which currently explains the
limitation to the operator.

**Done when** an operator uploads a PNG in the dashboard and a console loads
the resulting fest with that image visible in-game. Nothing short of the last
clause counts — a pack that builds, encrypts and decrypts can still contain a
texture the game renders as noise.

### 2.2 Internal asset names embed the ORIGINAL fest id — **P2, and a trap**

Reading a real custom pack showed every file inside named for the fest it was
derived from:

```
Mals/Msg_EUde.Fest_JUEA-DUMPLING_Bh.sarc.zs
Event/EventSetting/Fest_JUEA-DUMPLING_WinA.engine__event__EventSettingParam.bgyml
RSDB/Tag.Fest_JUEA-DUMPLING.dev.rstbl.byml.zs
```

That pack's `summary.yml` announces `JUEA-00210` / `juea-lovefes2`. So the
*container* is named for the custom fest while its *contents* are named for
Nintendo's `JUEA-DUMPLING` — because it was made by editing that fest.

**Nobody has established what the game actually keys on.** If it resolves
assets by the announced fest id, a genuinely from-scratch fest needs every
internal path renamed consistently *and* the RSDB/rstbl tables that reference
them rebuilt. If it only reads the pack it was handed, the names are inert.

**This needs a console, so an autonomous agent cannot settle it.** It is
already recorded in [`docs/QUESTIONS.md`](docs/QUESTIONS.md) with the assumption
being proceeded on (names are inert). Build §2.1 so that changing that
assumption later is a contained change rather than a rewrite — keep the naming
decision in one place.

### 2.4 Serving BCAT natively — **P1 to TEST, and the tooling is now built**

**The blocker here is smaller than earlier notes in this repository claimed.**

What was previously written down: Nintendo's delivery container carries an
RSA-2048 signature, therefore the console verifies it, therefore serving BCAT
means forging a Nintendo signature. **The middle step was never established.**
switchbrew documents the signature *field*; it says nothing about a
verification step or what happens on failure.

And a console running SwitchNet already has **sigpatches** — bypassing Nintendo
signature checks is the normal state of a CFW console, and `ROADMAP.md` §3.3
already depends on that for phase 3. Whether they reach the `bcat` sysmodule is
unknown: `sys-patch` covers `fs`, `es`, `ldr` and `nifm`, and `bcat` is not
listed. Unknown is not impossible.

#### Stage 1 — does the console reach us at all? **Built. Needs an operator.**

`internal/stubs/bcatprobe.go` logs every BCAT request in full — method, path,
query, headers, a hexdump of the body — and answers 200 with an empty body. It
is off by default and needs **both** halves to do anything:

```sh
switchnetctl hosts -bcat-probe > .../atmosphere/hosts/sysmmc.txt   # and emummc.txt
# stubs.bcat_probe: true   (or SWITCHNET_STUBS_BCAT_PROBE=1)
```

Then open Splatoon 3 and watch the stubs log. `cmd/stubs` warns loudly at
start-up when the probe is on, so a flag that did not take is obvious.

If requests arrive, the paths are right and DNS and TLS work — and the log is
the first record anyone has of what the console actually asks for. **Turn it
back off afterwards**: answering a console that expected content turns an
immediate failure into a hang, which is why those hosts are null-routed the rest
of the time.

#### Stage 2 — does it care about the signature? **Not built.**

Answer `/api/nx/v1/list/<name>` with a container that has the right 0x120 header
shape and a deliberately garbage signature. If the console proceeds, sigpatches
cover `bcat` and the signature is a non-issue. If it refuses, that is the first
real evidence either way.

Stage 1 has to run first, because its hexdumps are what say which path to answer
and what the console sent. Do not build stage 2 against a guess when stage 1
costs an operator five minutes.

**Even a positive result does not finish it**: the `list` response *body* is
still undocumented, and stage 1's hexdumps are how that gets chipped away. But a
positive result turns "nobody has documented how" into a tractable
reverse-engineering job instead of a closed door.

**Done when** there is a written record, under `docs/CAPTURES/`, of what the
console requested and how it reacted. Either answer is worth having; right now
the repository has neither.

**An autonomous agent cannot run stage 1** — it needs a console. The question is
recorded in [`docs/QUESTIONS.md`](docs/QUESTIONS.md).

### 2.4a The `.nro` installer — **P2, the path that works today**

A homebrew that fetches a seed from SwitchNet and writes it into Splatoon 3's
BCAT save data — what JKSV does by hand, automatically.

This exists because §2.4 is unresolved, **not** because serving BCAT is
forbidden. If §2.4 comes back positive this may become unnecessary; if it comes
back negative, this is the answer.

Note that the delivery cache is *downstream* of the wire protocol, so dumping a
console's BCAT save reveals the seed layout (already known) and **not** the
`list` response format. Do not expect a save dump to settle §2.4.

**No toolchain in this repository's dev container** — `DEVKITPRO` is unset and
there is no `aarch64-none-elf-gcc`, so an agent working here can write the
source and a Makefile but cannot compile or test it. Say so plainly if you
hand over untested C.

**Start at** `switch/`, and serve the seed over plain HTTPS from the dashboard
or a small endpoint on `cmd/npln`.

---

## 3. Dashboard

### 3.2 Splatfest asset editing — **P2, depends on §2.1**

The Splatfests tab creates, announces, holds and deletes fests. It does not
touch assets: splashart, tee designs, team names and idol dialogue all still
come from files built elsewhere.

The intended end state is doing all of it from the dashboard. §2.1 is the
blocker, not the UI.

### 3.3 Real authentication — **P2**

The dashboard's only protection is a shared token in config
(`dashboard.token`), and with it unset there is none at all: anyone who can
reach the port can schedule and cancel Splatfests.

That is deliberate rather than lazy — **nothing in phase 1 authenticates a
person.** Every credential in the stack belongs to a console, so there is no
user account to check against. A shared token is the honest minimum.

A real answer probably arrives with phase 3's Nintendo Account work, which
introduces the first human-facing login. Until then, do not build a bespoke
user system for the dashboard alone.

---

## 4. Verification and hardware

### 4.1 Certificate pinning — **P1, operator decision recorded**

Splatoon 3 carries its own TLS stack and pins its own certificates,
independently of the console's CA store — so phase 1's LayeredFS work does not
cover it.

`kinnay/NPLN-Protocols` ships `generate_patch.py`, which builds an IPS patch
disabling verification in the game's NSO. **The operator has accepted a
system-wide patch**, so this is a decision already made, not an open question.

**Done:** `make patch-splatoon3 NSO=<main>` builds the patch from the operator's
own dump and installs it for Ryujinx or an Atmosphère SD card. Nothing is
checked in and nothing can be: the address the patch writes to is found by
searching the dump, and the file has to be named for that build's module id, so
a prebuilt patch would be a patch for somebody else's copy. Both properties are
written down in the script's header and in `README.md` — it *disables*
verification rather than *adding* trust, and it is gated on the game version, so
a title update silently stops it applying.

Still unverified against a real dump: no Splatoon 3 NSO has been near this. The
script is exercised end to end against a synthetic NSO carrying the signature
upstream looks for, which proves the plumbing and the emitted IPS bytes but not
that the signature is present in the operator's build.

### 4.2 Nothing here has touched a console — **P1**

Every test in phase 2 runs against a Go client. That was deliberate, so the
work could proceed while §4.1 was open, but it means **no shape in this phase
is confirmed against hardware.**

The first console test is worth more than any amount of further server work.
Expect the JSON front end (§1.3) to be wrong first, because it is the least
documented and the earliest in the sequence.

### 4.3 Captures still wanted — **P2**

`grep -rn 'TODO(capture-needed)' internal/` for the live list —
`docs/CAPTURES/README.md` keeps the same list with what each would settle,
and is the one to trust when this paragraph and the grep disagree. As of
writing it is fourteen items and growing every time a new phase-2 service
gets registered against an undocumented shape rather than a real capture.

Two from the original five are still the ones that change behaviour rather
than cosmetics, not just naming: whether the game sends more than one
`UserDefinition` when a squad queues together (a squad currently has
everyone but the leader dropped), and whether it refuses to start a match
with an empty `MatchmakingIdToken` (a gap SwitchNet cannot fill without
issuing a credential nothing verifies). Newer arrivals worth the same
weight: `Messaging.RecvMessage`'s empty-resume-token behaviour (get it
backwards and an invite mailbox either loses invites silently or replays
stale ones on every reconnect), and whether a real client dials
`GameSession.Port` at all now that `SyncGameSession` exists to publish it.

Write captures up under `docs/CAPTURES/` using the template, redacted,
reconcile the implementation, and **delete the `TODO` comment in the same
commit** so the source and the notes cannot disagree.

---

## 5. Housekeeping

### 5.2 The `bcat-*` hosts are null-routed — **contingent, not settled**

`hosts.go` null-routes `bcat-list`, `bcat-data` and `bcat-topics`, and
`TestBlackholedHostsAreAccountedFor` pins that.

That is correct **while SwitchNet cannot answer them** — redirecting to a
server that fails to respond usefully turns an immediate failure into a hang.

It is not a permanent decision, and §2.4's experiment no longer requires
changing it: `switchnetctl hosts -bcat-probe` overrides the address at
*generation* time, leaving the classification in `hosts.go` — and the test that
pins it — untouched. That was deliberate. The flag is opt-in and temporary; the
classification is what the default deployment does.

A positive result from §2.4 would be the reason to change the classification
itself. Amend the test then, in a commit that says why, rather than treating
this entry as a rule.

### 5.3 A mode table for matchmaking — **P3**

`matchmaking.DefaultConfig.PlayersPerMatch` is still **2**, and per-mode sizes
are now settable at runtime from the dashboard and seeded from
`matchmaking.Config.ModeSizes`. So an operator can already make a mode the right
size without a code change.

What is still missing is knowing what the **faithful** sizes are. They belong in
the config's `ModeSizes`, driven by what a capture shows the game expects. Until
there is one, a number that lets two friends start a match beats one that is
correct and idle.

---
