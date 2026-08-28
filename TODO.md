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

### 1.1 The rest of `nn.npln.toyohr.v1.*` — **P2**

Every service is now registered, so an RPC with no handler answers
`codes.Unimplemented` and `internal/npln`'s interceptor logs it once, naming the
method. **That log is the to-do list** — it exists now, which it did not when
the services were unregistered.

Implemented: `FestService` in full, `Schedule` (versus and season; co-op is
capture-gated, see below), `LobbyMessaging`, `UserScreening`.

Still open, roughly in the order a console will ask for them:

| Service | What it needs beyond a handler |
| --- | --- |
| `GameRecord` | **the most valuable one.** Battle results come from here, and battle results are the missing half of a Splatfest verdict — see `GetFestResult` |
| `CloudSave` | be deliberate rather than quick: this holds the player's actual save, so a bug loses progress rather than degrading a feature |
| `CoopScenario`, `Replay` | a document store and a shared code namespace |
| `Locker`, `Canola` | the same document shape, per player |

Two things inside what *is* implemented are capture-gated rather than
unwritten:

- **`SelectCoopSchedules` answers empty.** A co-op shift is a stage, a boss,
  four weapon ids and a reward — all Splatoon 3 internal numbers, and unlike the
  versus rotation nothing useful is left once they are removed. An empty
  response is a state the game has a reading for; a shift full of invented ids
  is not.
- **`Schedule`'s stage and rule ids come from `npln.rotation`.** The mechanics
  around them are SwitchNet's; the numbers are not, so they are operator input.

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

### 2.4 Serving BCAT natively — **P1 to TEST, and the server is now ready**

**The blocker here is smaller than earlier notes in this repository claimed, and
it is worth an hour of an operator's time before any more is built around the
workaround.**

What was previously written down: Nintendo's delivery container carries an
RSA-2048 signature, therefore the console verifies it, therefore serving BCAT
means forging a Nintendo signature. **The middle step was never established.**
switchbrew documents the signature *field*; it says nothing about a verification
step or what happens on failure.

And a console running SwitchNet already has **sigpatches** — bypassing Nintendo
signature checks is the normal state of a CFW console, and `ROADMAP.md` §3.3
already depends on that for phase 3. Whether they reach the `bcat` sysmodule is
unknown: `sys-patch` covers `fs`, `es`, `ldr` and `nifm`, and `bcat` is not
listed. Unknown is not impossible.

**Stage 1 is now a five-minute job, and it needs a console.**
`internal/bcat.Probe` is written, wired into `internal/stubs`, and tested. To
run it:

1. change the three `bcat-*` entries in `internal/nintendo/hosts.go` from
   `ServiceBlackhole` to `ServiceStubs`;
2. move them out of `TestBlackholedHostsAreAccountedFor`'s map, in a commit that
   says why (that entry is explicitly contingent — see §5.2);
3. set `stubs.bcat_record_dir`, regenerate the DNS and nginx configuration, and
   open Splatoon 3.

Every request lands in the log and in a hexdumped transcript. If they arrive, the
paths are right and DNS and TLS work — and that transcript is the first record
anyone has of what the console actually asks BCAT for.

**Stage 2 — the deliberately-invalid signature — is deliberately NOT built.** It
only tests whether the signature is checked if the rest of the 0x120 header is
right, and the only public facts about that header are its size and the
signature's offset at 0x20. A guessed header produces a refusal nobody can
attribute, so a negative result would mean nothing. Stage 1's hexdumps are how
the header becomes writable; then this becomes a small commit.

**Done when** there is a written record, under `docs/CAPTURES/`, of what the
console requested and how it reacted. Either answer is worth having; right now
the repository has neither.

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

## 3. Dashboard

### 3.1 Match history and live matchmaking — **P2**

The overview has an "in matchmaking" tile that honestly reports *not wired yet*
rather than showing a zero that reads as "nobody is queuing". The queue is now
wired to the gateway and `Queue.Waiting()` returns exactly what the tile needs —
but the queue lives inside `cmd/npln` and the dashboard is a different process,
so reading it means a seam that does not exist yet. Add a match list at the same
time.

Needs an `npln_matches` table and store methods on **both** implementations,
with an entry in `internal/db/store_test.go`'s contract — that contract has
already caught two real divergences, so do not skip it.

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
Expect the JSON front end (`internal/nplnrest`) to be wrong first, because it is
the least documented and the earliest in the sequence. Set `npln.rest_record_dir`
before that first attempt: it writes every request and response to disk,
hexdumped and in order, so one boot produces the transcript that settles all
three of its shapes at once.

### 4.3 Captures still wanted — **P2**

`grep -rn 'TODO(capture-needed)' internal/` for the live list. As of writing:
every NPLN resource-name shape (collected in `internal/npln/names.go`, so one
capture settles all of them), whether Splatoon 3 ever calls `RefreshToken`, and
the three `internal/nplnrest` bodies — those last are what `npln.rest_record_dir`
exists to produce.

Write captures up under `docs/CAPTURES/` using the template, redacted,
reconcile the implementation, and **delete the `TODO` comment in the same
commit** so the source and the notes cannot disagree.

---

## 5. Housekeeping

### 5.1 `docs/ROADMAP.md` §2 is out of date — **P3**

[`docs/PHASE2_PLAN.md`](docs/PHASE2_PLAN.md) corrects it in four places — gRPC
is not first, three hosts were misidentified, BCAT is missing entirely, and
cert trust is a separate problem. The roadmap has not been amended, so the two
disagree.

Fold the corrections in and leave the plan as the record of how they were
found.
