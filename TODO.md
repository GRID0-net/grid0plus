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

### 1.3 The JSON front end: vermillion, penne, frontline — **P1 for hardware**

Splatoon 3 does **not** open with gRPC. It talks JSON over HTTPS to
`gw.hac.lp1.vermillion…` (device init) and `val.hac.lp1.penne…` (login tickets,
and the frontline FQDN), and only then opens the gRPC channel.

The hosts are already classified (`ServiceNPLNRest`) and the nginx generator
already emits their block; there is no `internal/nplnrest` package behind it.

**The frontline connection is a long-lived stream the console holds open.**
Closing it is an error on the console (`2307-2103` on Splatoon 2, `2321-*` on
Splatoon 3), not a clean end. The generated nginx block already sets
`proxy_buffering off` and a long read timeout for this.

**`TODO(capture-needed)`** on all three request/response shapes. The reference
implementation this project studied does not ship its captures, so nothing here
is known — implement the most defensible reading and say what a capture would
settle. Do **not** invent a plausible Nintendo internal.

### 1.4 The rest of `nn.npln.toyohr.v1.*` — **P2**

Roughly boot-to-match order: `Schedule` (map/mode rotation), `LobbyMessaging`,
`GameRecord`, `CoopScenario` (Salmon Run), then `Locker`, `CloudSave`,
`Replay`, `Canola`, `UserScreening`.

Treat it as a backlog and let the console drive the order: an unimplemented RPC
answers `codes.Unimplemented` and logs, and **that log is the to-do list**.

### 1.5 `gamesync` — **P2**

A Firestore-shaped document/watch service on its own port (7575 — the host is
already listed and `npln.gamesync_listen` already configured). Splatoon 3
reaches it directly rather than through the tenant host.

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

### 2.3 `switchnetctl fest build` — **P2**

One command that takes an assets directory plus a fest id, builds the packs,
derives the revision from their bytes, writes the seed folder, and registers
the matching fest — so the id, resource, revision and keys cannot drift apart.

Everything it needs exists: `bcat.BuildPacks`, `bcat.Revision`,
`Seed.Write`, `toyohr.Service.SaveFest`.

**Why it matters:** those four values agreeing is the entire contract with the
console. Any path where a human types one of them twice will eventually produce
`BcatInvalid`, which reaches the player as a generic communication error.

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

### 2.5 Fest results and voting — **P3**

`FestService` has `CreateFestEntry`, `GetFestEntry` and `GetFestResult`; only
`SelectFestSchedule` and `GetFestDecryptionKey` are implemented. Team choice
and the final verdict are unimplemented, so a fest currently runs but never
concludes.

`proto/toyohr/v1/resources.proto` has the full result shape
(`YobisaiResult`, `HonsaiMidtermResult`, `HonsaiFinalResult`, `OverallResult`).

---

## 3. Dashboard

### 3.1 Match history and live matchmaking — **P2**

The overview has an "in matchmaking" tile that honestly reports *not wired yet*
rather than showing a zero that reads as "nobody is queuing".

The queue is now served, so the live half is reachable: `Queue.Waiting()`
returns tickets per mode and exists for exactly this. It is on the **npln**
process, though, and the dashboard is a **separate** one — so this needs a way
across that boundary, the same problem `internal/presencebus` already solves for
presence with Redis. Do not add an in-process call that silently returns zero in
the deployed configuration; that is the failure `internal/dashboard`'s README
warns about, in a new place.

Match history is the other half and needs an `npln_matches` table plus store
methods on **both** implementations, with an entry in
`internal/db/store_test.go`'s contract — that contract has already caught two
real divergences, so do not skip it. `internal/matchmaking` keeps sessions in
memory for `SessionTTL` only; anything the dashboard should still show tomorrow
has to be written down when the match forms.

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

### 3.4 Per-player detail — **P3**

Clicking a player should show their friends, device accounts, presence history
and match history. `internal/db` has the friendship store; the rest is
assembly.

---

## 4. Verification and hardware

### 4.1 Certificate pinning — **P1, operator decision recorded**

Splatoon 3 carries its own TLS stack and pins its own certificates,
independently of the console's CA store — so phase 1's LayeredFS work does not
cover it.

`kinnay/NPLN-Protocols` ships `generate_patch.py`, which builds an IPS patch
disabling verification in the game's NSO. **The operator has accepted a
system-wide patch**, so this is a decision already made, not an open question.

Two properties to write down wherever it is documented: it *disables*
verification rather than *adding* trust, and it is gated on the game version —
a title update silently stops it applying.

### 4.2 Nothing here has touched a console — **P1**

Every test in phase 2 runs against a Go client. That was deliberate, so the
work could proceed while §4.1 was open, but it means **no shape in this phase
is confirmed against hardware.**

The first console test is worth more than any amount of further server work.
Expect the JSON front end (§1.3) to be wrong first, because it is the least
documented and the earliest in the sequence.

### 4.3 Captures still wanted — **P2**

`grep -rn 'TODO(capture-needed)' internal/` for the live list. As of writing:
the NPLN resource-name prefix in `IssueToken`'s response, whether Splatoon 3
ever calls `RefreshToken`, the matchmaking resource-name shapes, whether the
game sends more than one `UserDefinition` when a squad queues together, and
whether it refuses to start a match with an empty `MatchmakingIdToken`.

The last two are the ones that change behaviour rather than cosmetics: a squad
currently has everyone but the leader dropped, and an absent id token is a gap
SwitchNet cannot fill without issuing a credential nothing verifies.

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

`matchmaking.DefaultConfig.PlayersPerMatch` is **2**, not 8. On a private
network of a handful of consoles, waiting for eight players means never
playing.

The faithful sizes belong in a table keyed by mode, driven by what a capture
shows the game expects. Until there is one, a number that lets two friends
start a match beats one that is correct and idle.
