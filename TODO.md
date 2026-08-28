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

### 2.1 A texture converter — **P2, and the only remaining part is external**

**The pipeline is built; the converter is not, and probably should not be
written here.**

`internal/bcat` now has the conversion seam: a source asset is named for what it
must become (`UI/Splashart.bfres.png` → `UI/Splashart.bfres`), `bcat.converter`
is a command that does the conversion, its output is validated, and — the part
that mattered most — **a source asset with no converter configured is a build
error** rather than a PNG packed into the archive. The dashboard's Assets panel
reports the same thing.

What is left is a converter to point it at, and that means:

- **texture encoding** — BC1/BC3/BC7, plus the exact format each slot expects;
- **Tegra swizzling** — the GPU's block-linear memory layout;
- **BFRES authoring** — the container Nintendo's textures live in;
- **BYAML authoring** — team names and idol dialogue are structured data, not
  images.

**Do not write this in Go.** Switch Toolbox and the
`BinaryTextureLibrary`/`Syroot.NintenTools` family already do it, and they exist
because it is hard. The failure mode is the worst available: a texture that
*loads and renders as garbage*, on a console, with nothing on the server to look
at — so a reimplementation that is subtly wrong is worse than no
reimplementation. Wrapping one of those tools in a script that meets the
contract in `internal/bcat/convert.go` is the tractable job.

**Done when** an operator uploads a PNG in the dashboard and a console loads the
resulting fest with that image visible in-game. Nothing short of the last clause
counts — a pack that builds, encrypts and decrypts can still contain a texture
the game renders as noise. **That last clause needs a console, so an agent
cannot close this item.**

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
   says why — `hosts.go` and that test both say the null-route is contingent);
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

## 3. Dashboard

### 3.2 Splatfest asset editing — **P2, blocked on §2.1's converter**

The Splatfests tab creates, announces, holds and deletes fests, and now also
reports the delivery cache on disk — id, resource, revision and regions, read
from the seed's own `summary.yml` so a disagreement with the announced fest is
visible rather than hidden.

What it still does not do is edit assets. The intended end state is uploading a
PNG in the browser, and the blocker is §2.1's converter rather than the UI:
without one, an upload can only be stored, not turned into anything a console
reads. The Assets panel says so, with the naming rule and the reason SwitchNet
does not convert textures itself.

**When a converter exists**, this becomes an upload endpoint that writes into
`bcat.seed_dir`'s assets tree and shells out to
`bcat.BuildPacksWith` — everything below the UI is already in place.

## 4. Verification and hardware

### 4.1 Captures still wanted — **P2**

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
