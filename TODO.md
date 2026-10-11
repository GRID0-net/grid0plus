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

**The numbers are stable identifiers, not an ordering.** Items are referenced
from source comments, config and `docs/`, so a finished item is deleted and the
gap it leaves is kept rather than closed. §2.4 is §2.4 for as long as
`internal/bcat` and `hosts.go` point at it.

**What "capture" means here.** The operator does not run Atmosphère against
Nintendo's real servers, so a capture of Nintendo is a tcpdump at most:
hosts, timing, sizes, never decrypted content. A `TODO(capture-needed)`
shape is settled some other way — static RE of the game or sysmodule that
reads it, live GDB on GRID0+'s emuMMC (see
[`docs/CAPTURES/splat3-live-gamerecord-gate-2026-09-25.md`](docs/CAPTURES/splat3-live-gamerecord-gate-2026-09-25.md)),
a decrypted capture of the console talking to GRID0+, or a fact
NextendoNetwork has recorded from its own captures. Do not write "needs a
capture from Nintendo" as a plan; it will not happen.

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

## What is left, and why

What each open item is waiting on:

| Item | Blocked on |
| --- | --- |
| §4.4 | **a console.** Four one-boot re-tests of workarounds from 2026-09-25 |
| §4.5 | **the friend's console** — it stalls before any game call |
| §4.3 | **RE, GDB on the emuMMC, or tests against GRID0+** — never a Nintendo capture |
| §2.1 | **an external tool.** The pipeline is built; writing the texture converter in Go is explicitly the wrong answer |
| §2.4 | **the server's own `list`/`data` answers** — the signature patch that blocked it is built — then a console test |
| §3.2 | **§2.1** |

Splatoon 3 reached the hall on GRID0+ on 2026-09-25 (see
`docs/CAPTURES/splat3-hall-reached-2026-09-25.md`); what is left is behind
it — matches, Splatfests, BCAT.

---

## 1. NPLN protocol

### 1.3 The JSON front end: vermillion, penne, frontline — **P3, built and working on hardware**

`internal/nplnrest` serves vermillion, penne and the frontline connection on
`npln.rest_listen`; the dragons host's `PUT /v2/penne_id` is answered by
`internal/stubs`. Every endpoint NextendoNetwork/splatoon-3's REST gateway
serves is served here with the same shape, and a real console gets past
this stage to the tenant gRPC calls.

**Still open, none blocking:**

- What a frontline connection carries after its opening frame.
  `internal/nplnrest/frontline_frame.go` sends the size-prefixed FlatBuffer
  seen in the working Nextendo captures, then holds the connection open;
  nothing documents what, if anything, follows.
- Vermillion's `accounts/*`, `acds/search` and `nearness-checks` answer an
  empty `{"payload": ...}` envelope. No capture shows their real contents;
  Nextendo answers the same, because a 404 there makes the console restart
  its device bootstrap in a loop.

Both are readers-side questions: the frontline is parsed by a system
client (`npns` is the leading candidate, not the game — see the 2026-09-20
decompile note in `internal/nplnrest/nplnrest.go`), and the vermillion
payloads by whichever module issues them. RE of that module settles each.
Do **not** invent a plausible Nintendo internal before then.

### 1.4 The rest of `nn.npln.toyohr.v1.*` — **P2, mostly done**

Every toyohr service Splatoon 3 calls is registered (`internal/toyohr/service.go`).
Where each stands:

- **`Schedule`**: all five RPCs serve real rotations. Versus, Season and
  VsParams follow the working Nextendo captures' shape (previous slot first,
  one shared `schedule_set_id`, quoted 24-character ETags, VsParams tuning
  attributes). Salmon Run is operator-scheduled or auto-filled by
  `coop_generate.go`. League answers empty unless the disposable
  `league_diagnostic.go` blob is enabled — GRID0+ has no authored League
  events.
- **`FestService`**: schedule, keys and results are real. An empty message is
  the answer when no fest is running, as in both working captures.
  `SelectFestBreakingNews`, missing from the proto, answers the empty
  message Nintendo sent during a real Splatfest.
- **`CloudSave`**: persists each player's save record (`save_store.go`).
  `WriteSaveRecord` deltas are merged the way Nintendo's server merges them;
  `CreateSaveRecord` stores a new console's record.
- **`GameRecord`**: `InitializeAttributes` creates the Vs/Coop attribute
  document with only the fields the request carries;
  `InitializeTag`/`GetTag`/`SelectTags` acknowledge without storing (they
  carry a League schedule and rule; GRID0+ authors no League events);
  battle results, Salmon Run results, point cards and grades are written by
  the server (see `internal/ugcstore/README.md`).
- **`LobbyMessaging`**: real, backed by `messaging.go`'s per-lobby hub. The
  self-addressed `NplnLogin` echo the hall waits for is covered by
  `TestSelfAddressedNplnLoginIsEchoedBack`.
- **`Canola`/`Locker`**: `SelectDocuments` serves the plaza (newest post
  per player), name plates for named players (`Normal3`) and the fest plaza's
  drawings (`Fest`); `RegisterDocument` acknowledges; `IkasuDocument` (a
  like) acknowledges a real post. Posts and their images go through
  Ugcstore and its attachment uploads.
- **`CoopScenario`/`Replay`**: `RegisterDocument` issues share codes and
  `Resolve*Code` resolves them; `Replay.FulfillReservations` answers an
  empty list.
- **`UserScreening`**: `GetViolation`/`SelectSnapshot` answer `NotFound`
  (Nintendo's own answer for `GetViolation`, per Nextendo's captures);
  `CreateReport` is accepted and logged.

An unimplemented RPC answers `codes.Unimplemented` and logs, and **that log is
the to-do list** for whatever remains.

### 1.5 `gamesync` — **P2, built**

`internal/gamesync` implements almost all of `nn.npln.gamesync.v1.Gamesync`:
`IssueToken`/`RefreshToken`, `GetDocument`/`ReadDocuments`/`ListDocuments`/
`QueryCollectionIds`, `WriteDocuments`/`LazyWriteDocuments`,
`BeginTransaction`/`CommitTransaction`/`RollbackTransaction`, and the
`KeepUserSession` watch stream. `CreateRound` is left `Unimplemented`
deliberately — see below.

**Its own port, not the tenant channel.** `gamesync.npln.nintendo.net` not
ending in `srv.nintendo.net`, unlike every other NPLN host, turned out to be
exactly the deliberate signal it looked like: `internal/nintendo/hosts.go`
classifies it `ServiceGamesync`, a distinct service on its own port (7575,
`npln.gamesync_listen`) rather than grouped with the tenant host's nginx
block. `internal/gamesync` is a complete, self-contained `*grpc.Server` with
its own listener in `cmd/npln` — not a service registered on
`internal/npln`'s gateway — verifying the matchmaking id token the gateway
signs via a `TokenVerifier` callback rather than an import (which would be a
cycle). See `internal/gamesync/README.md` and `docs/QUESTIONS.md`'s now-
resolved entry on this.

**What's refused rather than guessed at:** `FieldTransform`'s
`LoadGlobalValue`/`StoreGlobalValue`/`ClearGlobalValue` read and write a
namespace shared across documents that nothing here models — treating them
as no-ops would have a client store a value it later reads back as absent,
so a transform batch containing one is refused whole (`codes.Unimplemented`)
before any write in it applies, the same atomicity guarantee a transform
this code understands gets. `WriteDocuments`/`CommitTransaction`'s
`DefermentOperation` (a batch of writes to apply later, on a trigger nothing
here models) is refused the same way — accepting one and silently dropping
it would tell a client "done" for an effect that never arrives. `CreateRound`
is left entirely to the embedded `Unimplemented`: `Round` has five fields,
four of them named `Unk1` through `Unk4` in this project's own proto
reversal, and answering would mean inventing values for fields whose meaning
is unknown — worse than a logged `Unimplemented`.

---

## 2. Splatfests and BCAT

### 2.1 Asset conversion: images → game-ready textures — **P2, large, texture piece built**

**The pipeline is built; the converter is not, and probably should not be
written here.**

`internal/bcat` takes assets that are *already* in the game's formats
(`.bfres`, `.byaml`, `.sarc.zs`), assembles them into a SARC archive and
encrypts it. What is wanted is uploading a **PNG of the splashart, the tee
designs, the banners** and having the server produce a loadable pack. Of the
four pieces that needs, one is now built:

- **Texture encoding + Tegra swizzling — the raw pixel piece is built, by
  shelling out, not reimplementing.** `internal/bcat.ConvertTextures`
  (`texture.go`) and `switchnetctl fest build -source-images
  -texture-converter` turn a directory of PNGs into `.bntx` files by
  calling an external tool -- verified against a real, built copy of
  [jam1garner/img2bntx](https://github.com/jam1garner/img2bntx), not
  assumed from its README: its BNTX header layout (revision `0x400c`
  included) matches a real Splatfest's own embedded texture archive **byte
  for byte** (checked 2026-09-03 against `JUEA-00210`'s real pack). This
  only produces uncompressed RGBA8 textures, no BC1/BC3/BC7 -- larger
  files, but nothing to get a compression format wrong about.
- **BFRES authoring — confirmed necessary, not yet built.** A real
  Splatfest's only visual asset is one `FRES` container (Switch/NX BFRES,
  not the Wii U-era format most public BFRES libraries target) with a
  single embedded multi-texture BNTX archive and no model data. So
  `ConvertTextures`'s bare `.bntx` is not, on its own, what a console loads.
  Building the FRES wrapper from scratch needs the outer container's
  dictionary/relocation structure fully understood first, which this
  project could not do with confidence from one sample and no hardware to
  test a wrong guess against -- see `internal/bcat/README.md`'s texture
  section for exactly what was and was not verified, and the safer
  same-dimension-replacement approach recommended there as the next step.
- **BYAML editing** — team names and idol dialogue live in structured data
  inside the pack, not in the images. Still not built.

**Start at** `internal/bcat/texture.go` (`ConvertTextures`) for the texture
piece, and `internal/bcat/README.md`'s texture-conversion section for what
building the FRES wrapper actually needs; the dashboard's Assets panel
(`internal/dashboard/templates/index.html`) still explains the remaining
limitation to the operator and has not been wired to this yet.

**Done when** an operator uploads a PNG in the dashboard and a console loads
the resulting fest with that image visible in-game. Nothing short of the last
clause counts — a pack that builds, encrypts and decrypts can still contain a
texture the game renders as noise, and that clause needs the operator's own
console and a real dumped pack to compare against, not more server code.

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

**Nobody has established what the game actually keys on, against GRID0+'s
own hardware.** If it resolves assets by the announced fest id, a genuinely
from-scratch fest needs every internal path renamed consistently *and* the
RSDB/rstbl tables that reference them rebuilt. If it only reads the pack it
was handed, the names are inert.

**This needs a console, so an autonomous agent cannot settle it outright** —
but it now has independent corroboration rather than just a guess.
`NextendoNetwork/splatoon-3`, a from-scratch NPLN server the operator
supplied and that repository says was tested against real hardware
(PolyForm Shield-licensed — this design fact only, never its code), depends
on exactly "names are inert" in production: unable to synthesise Nintendo's
encrypted packs either, it announces whatever fest identity a console's
*existing* BCAT cache already holds (a hot-reloadable id/resource/revision
rewrite over its served schedule) rather than ever renaming a pack's
internal files to match. See
[`docs/QUESTIONS.md`](docs/QUESTIONS.md) for the full citation. Build §2.1
so that changing the assumption later, if GRID0+'s own hardware disagrees,
is a contained change rather than a rewrite — keep the naming decision in one
place.

### 2.4 Serving BCAT natively — **done, working on hardware**

**State (2026-10-11):** both halves are built. The signature check in the
`bcat` sysmodule is skipped by a firmware 22.5.0 IPS patch shipped through
the Toolbox (d24f066), and `internal/bcat` serves the list and data
responses from GRID0+'s own hosts. On 2026-10-09 a real console downloaded
all five packs of a custom Splatfest natively from grid0. What follows is
the investigation as it was written, kept for its reasoning; a new firmware
needs the patch re-derived the same way.

**The goal is a vanilla path:** the console downloads fest packs and other
delivery-cache content from GRID0+'s own `bcat-list`/`bcat-data` hosts,
the same way it downloads them from Nintendo, with no files handed to the
player and no homebrew installer. §2.4a is the fallback, not the plan.

Nobody is known to do this. NextendoNetwork (checked 2026-09-25 across
splatoon-3, nextendo-account and Prelude-Nro) does not serve the live BCAT
protocol either: its account server hands its homebrew a zip
(`/api/bcat/<titleId>`), and Prelude only redirects the three BCAT hosts so
consoles stop presenting a Nextendo-minted token to Nintendo. Public tools
(ACNH event packs, Switch Gift Data Manager) forge packages into the
delivery-cache save through JKSV, never over the network.

**What blocks it is one question: does the console verify the container's
RSA-2048 signature, and where?** switchbrew's BCAT Content Container page
documents the 0x120-byte header (magic `bcat`, crypto type, RSA/hash type,
secret-data index, IV, 0x100-byte signature) and the key derivation
(PBKDF2-HMAC-SHA256 over the title's `control.nacp` passphrase, salt
`%016llx%s` of title id and a secret string, 4096 rounds). It does not say
what the signature covers, which key checks it, or what happens when it
fails. The key derivation is enough to build a correctly *encrypted*
container; only the signature stands in the way.

**RE target — not yet pinned down, so check each:**

1. **The `bcat` sysmodule** (title `010000000000000C`, `bcat.nso`, already
   extracted from the operator's SYSTEM partition for the 2026-09-20 list
   parser work). The most likely place: switchbrew says files are decrypted
   before the sysmodule hands them over, so whatever checks the signature
   plausibly runs there, before or beside the decrypt. Look for the RSA
   verify around the container-header parse, the public key(s) it uses, and
   the branch taken on failure.
2. **A crypto helper the sysmodule calls into** — `spl`/`es`-side RSA
   services, if the check is delegated rather than done in-process. If so,
   the sigpatches question (`sys-patch` covers `fs`, `es`, `ldr`, `nifm`, not
   `bcat`) changes shape.
3. **The game itself** (Splatoon 3 `main`) is the least likely: it reads the
   delivery cache through `nn::bcat` IPC and gets plaintext. It is still
   where the fest pack's *own* encryption (nisasyst, key from
   `GetFestDecryptionKey`) is handled, which GRID0+ already controls.

**What the answer decides:**

- *No verification, or verification a patch can skip:* build the list and
  data responses (the list JSON field names are already known, see below)
  and serve GRID0+'s own containers. A one-instruction IPS patch against
  `bcat.nso`, shipped the same way as the Splatoon 3 TLS patch, is
  acceptable; forging Nintendo's signature is not (CLAUDE.md, rule 2).
- *Verification that cannot be skipped:* §2.4a.

Stages 1 and 2 below are the console-side checks. Stage 1 still costs an
operator five minutes and gives the first transcript of what the console
asks for; run it alongside the RE, not after it.

#### Stage 1 — what does the console ask for? **Built. Set one config line.**

The three `bcat-*` hosts already reach `internal/stubs`, which answers 304
(anything else risked the 2122-2403 hard error). Set
`stubs.bcat_record_dir` and every one of those requests is also recorded by
`internal/bcat.Probe` — method, path, query, headers, hexdumped body —
without changing the answer. One boot of Splatoon 3 then gives the list and
data paths and names the console requests.

#### Stage 2 — does it care about the signature? **Not built.**

Answer `/api/nx/v1/list/<name>` with a container that has the right 0x120 header
shape and a deliberately garbage signature. If the console proceeds, sigpatches
cover `bcat` and the signature is a non-issue. If it refuses, that is the first
real evidence either way.

Stage 1 has to run first, because its transcript is what says which path to
answer and what the console sent.

**Update, 2026-09-20: the `list` response body's field names are no longer
undocumented.** A decompile of `bcat.nso` itself (extracted from the
operator's own SYSTEM partition, title id `010000000000000C`) found the
complete JSONPath field list its response parser reads --
`directories[].{name,mode,digest,data_list[]}` and
`data_list[].{data_id,filename,digest,size,url,default_language,
deletion_priority,publishing_time,version.{format,semantics},
conditions.serial[],languages[]}` -- written up in
`internal/bcat/probe.go`'s own doc comment. Field *names* are confirmed;
most field *semantics* (what `mode`'s values mean, what `conditions.serial`
matches) and whether the wire encoding is JSON or MessagePack (a local cache
path, `list.msgpack.bafmeta`, suggests the latter but doesn't prove it) are
still open, and the delivery container's binary header remains exactly as
undocumented as before -- decompiling the *response parser* doesn't touch
the *signature verification* stage 2 is about. Stage 1 is still the thing
that needs an operator and a console; this only means stage 2's payoff, if
run, would land on a server that already knows most of what to answer with.

**Update, 2026-09-26: most of the wire format is now known, from a public
client of the live servers.** [BCAT-Manager](https://github.com/Random06457/BCAT-Manager)
downloads and decrypts real lists from Nintendo's CDN. It has no licence, so
these are facts read from it, not code taken from it:

- **List path:** `bcat-list-lp1…/api/nx/v1/list/nx_data_<title id, 16 lowercase
  hex>`. No token is sent; the request carries
  `User-Agent: libcurl (nnBcat; <uuid>; SDK <a.b.c.d>)` and
  `X-Nintendo-Serial-Number`.
- **The list body is itself a `bcat` container,** and its decrypted payload
  is **MessagePack**. That settles the JSON-vs-MessagePack question above.
  Top-level fields: `topic_id`, `service_status`, `na_required`,
  `required_application_version`, `directories`. Each directory also has
  `by_country_group`. Digests are 32 hex characters (16 bytes). The tool's
  default `mode` is `"async"`.
- **Container header (0x120 bytes):**

  | offset | size | field |
  | --- | --- | --- |
  | 0x0 | 4 | magic `bcat` |
  | 0x4 | 1 | version, written as 1 |
  | 0x5 | 1 | crypto: 1/2/3 = AES-128/192/256-CTR, 4 = plaintext |
  | 0x6 | 1 | signature hash: 0 = SHA-1, 1 = SHA-256 |
  | 0x7 | 1 | secret index, 0-31 |
  | 0x8 | 8 | padding |
  | 0x10 | 0x10 | CTR IV |
  | 0x20 | 0x100 | RSA-2048 signature |

  The payload follows at 0x120.
- **Key:** PBKDF2-HMAC-SHA256, 4096 rounds. The password is the title's
  passphrase as ASCII. The salt is the 16-hex-digit title id followed by
  secret string `index`, one of 32 16-hex-character strings. Those strings
  are Nintendo's; like the nisasyst table, they would live in an
  operator-local file and never be committed.

**Update, 2026-09-26: `bcat.nso` (22.5.0, build id 6D9772A733E2370B…) was
disassembled and the two questions that decide the design are answered.**
The operator supplied the firmware; the module was extracted with
`switchnet_bcat_extract.py` and read in Ghidra. No Nintendo key bytes are
recorded here or committed anywhere.

1. **The signature is verified, and its failure is enforced.** After a
   container is decrypted, the module runs an RSA-2048 verify over the
   payload against a public key baked into `bcat.nso` (a production key and
   a development key, chosen by the `bcat!production_mode` setting). On a
   failed verify it wipes the decrypted buffer and returns `0x2A07A`
   (result 2002-4106). Both the whole-file path and the streaming path do
   this; no input value skips it. So serving a container GRID0+ produced
   needs **either** Nintendo's private key (impossible, and forging it is
   forbidden by rule 2) **or** a patch to `bcat.nso` that forces the verify
   result to pass — the same class of patch this repo already ships for
   Splatoon 3's TLS, produced the same way (borntohonk's `find_patterns` /
   `make_patches`). That patch is the whole decision, and it is now known to
   be the *only* blocker.
2. **Plaintext containers are accepted; no passphrase or secret strings are
   needed.** The 0x120 header validator accepts crypto type 0, and the
   cipher setup treats 0 as "no encryption" (1/2/3 are AES-128/192/256-CTR).
   The `crypto > 3` guard means BCAT-Manager's "type 4 = plaintext" reading
   is really "type 0 = plaintext, written by a tool that stored index+1".
   GRID0+ can therefore serve type-0 containers and skip the whole
   PBKDF2/secret-string layer; only the signature stands in the way.

Still open, and less important than the two above:

3. **Which hash the list's 16-byte `digest` is,** and whether the console
   rejects a mismatch. (Splatoon 3's own delivery-cache digest is MD5 in
   Ryujinx/Citron HLE, but bcat's list digest is a separate field.)
4. **Whether a `url` may name a host other than `bcat-data-lp1`.**

The verify patch is tied to one build of `bcat.nso`, so it must be made
against the firmware the consoles run. This one is 22.5.0; a firmware
update needs the patch regenerated.

**Done when** GRID0+ answers the `list` and `data` requests and a patched
console downloads and reads a GRID0+-built container, with a written
record under `docs/CAPTURES/`. Both questions that blocked this are settled
and the signature-bypass patch is built
(`switch/atmosphere/exefs_patches/bcat_signature_bypass/`) and shipped to
consoles through the GRID0+ Toolbox's `romfs/sd/` mirror from v0.5.9; what
remains is the server's own `list`/`data` answers, and the console test that
nothing here can substitute for.

**Stage 1 (what the console asks for) still needs a console**, and is
recorded in [`docs/QUESTIONS.md`](docs/QUESTIONS.md); it is no longer on the
critical path, since the container format and the blocker are both known.

**The full implementation plan is [`docs/BCAT_NATIVE.md`](docs/BCAT_NATIVE.md)**
— the server (list/data responses from the existing seed folders), the
`bcat.nso` signature-bypass patch, the emulator-first verification path, and
the small open questions. The patch is built and shipped; the server side is
not.

### 2.4a The `.nro` installer — **P2, the path that works today**

A homebrew that fetches a seed from GRID0+ and writes it into Splatoon 3's
BCAT save data — what JKSV does by hand, automatically.

This exists because §2.4 is unresolved, **not** because serving BCAT is
forbidden. If §2.4 comes back positive this may become unnecessary; if it comes
back negative, this is the answer.

**Independent corroboration, 2026-09-05:** `NextendoNetwork/nextendo-account`
and `NextendoNetwork/splatoon-3` (real, source-available reimplementations
the operator supplied; PolyForm Shield-licensed, so only this design fact is
taken, never their code) do not serve native BCAT over the wire either, for
either of their two titles that use it (Splatoon 2's NEX BCAT and Splatoon
3's Splatfest packs). Both instead relay a pre-dumped delivery-cache
directly into the console's save data via their own homebrew installer,
verbatim, and never attempt to answer the live `bcat-*` protocol at all.
That is exactly this section's approach, arrived at independently -- it is
not just "the path that works today" for lack of a better one, it appears to
be what a real production service settled on too, which raises this
section's priority relative to §2.4's still-open signature question.

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

### 4.4 Re-test the 2026-09-25 workarounds — **P2, one boot each**

The hall failures of 2026-09-25 turned out to be one bug: `content-length: 0`
on gRPC replies, which nginx's `grpc_pass` turns into a reply without its
status, read by the console as UNKNOWN (see
[`docs/CAPTURES/splat3-hall-reached-2026-09-25.md`](docs/CAPTURES/splat3-hall-reached-2026-09-25.md)).
Several changes made while chasing it may have been workarounds for that bug
rather than real requirements. Now that `NotFound` reaches the console, each
is worth one boot, one at a time, moving back toward Nintendo's captured
shape:

1. **`WeaponPowerMeasurement/<season>` absent → `NotFound`** (the captures'
   answer) instead of a named, empty document (`emptyWhenAbsent` in
   `internal/ugcstore/service.go`).
2. **`GetSaveRecord` with nothing stored → zero messages** (Nextendo's
   measured Nintendo answer) instead of an empty record
   (`internal/toyohr/cloud_save.go`). Watch that an established console still
   writes its save afterwards.
3. **Schedule ETags in the captured `"<V|C|S|P><16 hex>-<4 hex>"` format.**
   Committed as `f6c495f` and reverted (`8a790ed`) only while bisecting; it was
   not the cause. Re-apply with `git revert 8a790ed`.
4. **Documents named with the real tenant** (`tenants/t-dce9377b-lp1/...`)
   rather than the request's `current` alias, as in both captures. Committed as
   `ca55503`, reverted (`9dbcb49`) only while bisecting. Re-apply with
   `git revert 9dbcb49`.

Also noted, not urgent: GRID0+'s Salmon Run shifts are 40-hour slots from
the Unix epoch (boundaries 00:00/16:00/08:00 UTC); Nintendo's in the captures
fall 14 hours later (10:00/02:00/18:00 UTC). Only the timing differs.

**Done when** each shape either matches the capture on hardware or has a
written reason it cannot.

### 4.5 A friend's console stalls before the hall — **P1**

A second console (account Musi, `7b8cd8861212a1b8`) signs in, gets a game
session (`IssuePrearrangedUserToken`) and `ListLatencyMeasurementServers`,
then sends only `Auth/ValidateToken` every ~10 s and shows 2123-8028. It never
opens `RecvMessage` or asks for schedules, unlike a working console. The next
step for a working console is measuring latency over UDP to the addresses
`ListLatencyMeasurementServers` returned (STUN, `internal/stunresponder`).
The `content-length` fix does not reach this point, since none of those calls
is made yet.

**Next:** a VM-side packet capture while that console boots (any host, UDP
included) to see whether its STUN packets arrive and are answered.

### 4.3 Undocumented shapes — **P2**

`grep -rn 'TODO(capture-needed)' internal/` for the live list;
`docs/CAPTURES/README.md` keeps the same list with what each would settle
and is the one to trust when they disagree. The tag name is historical: see
"What capture means here" at the top — each one is settled by RE, GDB on
the emuMMC, or a decrypted capture against GRID0+, not a Nintendo
capture.

The ones that change behaviour rather than cosmetics:

- **Squads.** Whether the game sends more than one `UserDefinition` when a
  squad queues together (a squad currently has everyone but the leader
  dropped). Settle it from the game: the matchmaking request builder in
  Splatoon 3 `main`, or a squad queue against GRID0+ with the request
  logged.
- **`Messaging.RecvMessage` with an empty resume token.** Get it backwards
  and an invite mailbox either loses invites silently or replays stale ones
  on every reconnect. A console reconnecting to GRID0+ shows which it
  expects.
- **`GameSession.Port`.** Whether a real client dials it at all now that
  `SyncGameSession` publishes it: a match on GRID0+ with tcpdump on the
  VM answers it.
- **Eight `accounts.nintendo.com` paths** the `account` sysmodule can build
  and GRID0+ does not serve (`/profile`,
  `connect/1.0.0/api/term_agreement_token`, `connect/1.0.0/term_confirm`,
  three `connect/1.0.0/api/transfer_na_tokens*`, both
  `analytics/device/opt_in` forms; see
  [account-sysmodule-decompile.md](docs/CAPTURES/account-sysmodule-decompile.md)).
  The nginx access log during one link attempt against GRID0+ says
  whether any is requested.

Write findings up under `docs/CAPTURES/` using the template, redacted,
reconcile the implementation, and **delete the `TODO` comment in the same
commit** so the source and the notes cannot disagree.

---

## 5. Housekeeping

### 5.3 A mode table for matchmaking — **P3, mostly done**

Versus modes default to eight (`npln.players_per_match`) as a cap: an even
count below it starts after the grace period, so two friends still get a
match. The three Salmon Run configs default to four and coop-shaped, built
into `config.Default`, so a fresh install no longer needs `mode_players` /
`coop_modes` to form Salmon Run correctly. Anarchy Series' one-sided lobbies
of four are handled in `internal/matchmaking/bankara.go`.

Still read from nothing but the config: the per-kind Splatfest lobby sizes
(see `internal/matchmaking`'s fest forms), which come from the game's own
code rather than its parameter tables.

---
