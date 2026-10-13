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
| §1.6 | **the bot command** — server side built (`internal/splat3save`, `POST /bot/v1/splatoon3/save/import`); the operator installs the table file |
| §4.4 | **a console.** Four one-boot re-tests of workarounds from 2026-09-25 |
| §4.3 | **RE, GDB on the emuMMC, or tests against GRID0+** — never a Nintendo capture |
| §2.1 | **an external tool.** The pipeline is built; writing the texture converter in Go is explicitly the wrong answer |
| §4.6 | **nothing now** — the licence publish/revoke loop is Phase 3 scope; watch the logs |
| §3.2 | **§2.1** |
| §2.4 | **nothing** — open edges only; the patch needs re-deriving on a firmware update |

Splatoon 3 reached the hall on GRID0+ on 2026-09-25 (see
`docs/CAPTURES/splat3-hall-reached-2026-09-25.md`); what is left is behind
it, and matches, Salmon Run, Splatfests and native BCAT have since worked on
hardware too.

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

### 1.6 Upload a Splatoon 3 `save.dat` through the Discord bot — **P2, server side built, bot command left**

**What:** a `/splatoon-save-upload file:<save.dat> [username:]` command in
`n-popescu/grid0plus-discord` that replaces the player's CloudSave record
with the one inside their own save, so a save made elsewhere (another
server, an edited save, an old dump) counts on grid0 as if it had been
played there.

**Why it is needed:** Splatoon 3 keeps two copies of progress. Multiplayer
data (rank, money, weapons, gear, Splatfest records…) belongs to the
account and lives on the server, in the record `internal/toyohr/save_store.go`
stores; the server copy wins, and Flexlion's save editor warns that edits to
those fields are reset when the game connects. The console only uploads what
changes (after `/splatoon-save-wipe` it wrote 3 keys, 95 bytes), so putting a
different `save.dat` on a console never fills the server record.

**What is known:**

- A decrypted save has two halves, `server` and `client` (Flexlion's
  `saveeditor.js` reads `SaveJson["server"]["UserName"]`, `["PlayerRank"]`,
  `["Money"]`, `SaveJson["client"]["Common"]…`). The `server` keys are the
  CloudSave record's own top-level keys
  ([`docs/CAPTURES/splat3-save-shape-2026-09-24.md`](docs/CAPTURES/splat3-save-shape-2026-09-24.md)),
  so the import is: decrypt, take `server`, convert to
  `nn.npln.common.Value`, store. The `client` half never travels over the
  network; the player keeps it on the console (JKSV, emulator).
- Flexlion decrypts on its own server (`flexlion3.herokuapp.com/save/decrypt`);
  its page source has no crypto. No public Splatoon 3 save tool was found.
- Splatoon 2's scheme ([SciresM's splatsave](https://gist.github.com/SciresM/27f66899b9504343c32b71f05368c654)):
  0x10 plaintext header (`u64 version`, CRC32), AES-128-CBC body, footer
  `IV | key seed | AES-CMAC`, keys from `sead::Random` seeded with the key
  seed over a 64-word table from the game binary; later versions add a
  CRC-seeded block shuffle. `internal/bcat/seadrand.go` already implements
  the generator.
- **Splatoon 3 is not that layout.** A real 11.3.0 save (93,951 bytes) has
  no plaintext header (entropy 7.99 bits/byte from byte 0) and a size that is
  not a multiple of 16 (0x16EF0 + 15), so the format has to be read from the
  game.

**The encryption is solved (2026-10-13):** `[u32 seed][u32 check]` +
AES-256-CTR, key and IV drawn from `sead::Random` seeded off a 32-word
table in `main`; verified on three real saves. See
[`docs/CAPTURES/splat3-save-dat-format-2026-10-13.md`](docs/CAPTURES/splat3-save-dat-format-2026-10-13.md).
**The server record does win over a local save.** NextendoNetwork's own
notes (facts only, `cloudsave_nextendo.go`) record that when they seeded
every new player's server record from one captured account ("gen", Money
2658, PlayerRank 1), every player, their main account included, came up
with that account's name, level, money, gear and ranks despite having
local saves; their server-side save editor relies on the same. GRID0+'s
consoles call `GetSaveRecord` on every boot and only upload their own save
when the record is empty. So replacing the record is enough for an import;
a one-boot check on hardware is still worth doing, but nothing waits on it.

**The server side is built (2026-10-13).** `internal/splat3save` decrypts,
checks, parses the MessagePack body and converts its server half to a
`MapValue` with field names and value types the stored records use (checked
path by path against a real record; see its README for how id maps are
told apart). `POST /bot/v1/splatoon3/save/import` (`{username?, save:
<base64>}`) archives the current record, then replaces it whole, moving its
version forward. It is off until `baas.bot_api.splatoon3_save_table` names
the operator's 128-byte table file. Refusals: `not_a_save` (check word),
`bad_save` (header, parse, or a field `names.txt` does not know).

Left:

1. Install the table on grid0 (`/opt/switchnet/secrets/`) and set the key.
2. The bot command in `grid0plus-discord`, with a confirm button and
   "close Splatoon 3 first" (a running game would write over the import).

**Rules:** the key table, any constant lifted from `main` and every real
save stay out of the repository (CLAUDE.md rule 1); load them from an
operator file the way `internal/bcat/nisasyst.go` loads its table. Treat an
upload as untrusted: size cap, MAC check, refuse unknown top-level keys.

**Done when** a player uploads a save through the bot, boots the game on
grid0, and sees that save's rank, money and gear survive going online.

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

### 2.4 Serving BCAT natively — **done, working on hardware; open edges below**

**State (2026-10-11):** both halves are built. A firmware 22.5.0 IPS patch
(`switch/atmosphere/exefs_patches/bcat_signature_bypass/`, shipped through
the Toolbox from v0.5.9, d24f066) skips the `bcat` sysmodule's signature
check, and `internal/bcat` (`native.go`) serves the `list` and `data`
responses from GRID0+'s own hosts. On 2026-10-09 a real console downloaded
all five packs of a custom Splatfest natively from grid0.
[`docs/BCAT_NATIVE.md`](docs/BCAT_NATIVE.md) is the design it was built
from.

**The facts it rests on** (from BCAT-Manager, read not copied, and a
Ghidra read of `bcat.nso` 22.5.0, build 6D9772A733E2370B…; no Nintendo key
bytes recorded anywhere):

- **List path:** `bcat-list-lp1…/api/nx/v1/list/nx_data_<title id, 16
  lowercase hex>`, no token; `User-Agent: libcurl (nnBcat; <uuid>; SDK
  <a.b.c.d>)` and `X-Nintendo-Serial-Number`. The body is itself a `bcat`
  container whose payload is **MessagePack**: `topic_id`, `service_status`,
  `na_required`, `required_application_version`, `directories[]` (each
  with `by_country_group`), field names confirmed by `bcat.nso`'s own
  parser (`internal/bcat/probe.go`).
- **Container header (0x120 bytes):**

  | offset | size | field |
  | --- | --- | --- |
  | 0x0 | 4 | magic `bcat` |
  | 0x4 | 1 | version, written as 1 |
  | 0x5 | 1 | crypto: **0 = plaintext**, 1/2/3 = AES-128/192/256-CTR |
  | 0x6 | 1 | signature hash: 0 = SHA-1, 1 = SHA-256 |
  | 0x7 | 1 | secret index, 0-31 |
  | 0x8 | 8 | padding |
  | 0x10 | 0x10 | CTR IV |
  | 0x20 | 0x100 | RSA-2048 signature |

  Plaintext containers are accepted, so the PBKDF2 passphrase/secret-string
  layer (PBKDF2-HMAC-SHA256, 4096 rounds, salt = title id + secret string)
  is never needed. Those strings are Nintendo's and would never be
  committed.
- **The signature is enforced** (RSA-2048 against a key baked into
  `bcat.nso`; failure wipes the buffer and returns 2002-4106, on both the
  whole-file and streaming paths). The patch forces the verify result to
  pass. Forging a signature is forbidden (rule 2) and impossible.

**Still open:**

1. **A firmware update breaks it.** The patch matches one `bcat.nso` build;
   a new firmware needs it re-derived the same way (borntohonk's
   `find_patterns`/`make_patches`) before consoles update.
2. **Which hash the list's 16-byte `digest` is,** and whether a mismatch is
   refused. It works today; nobody has checked what it tolerates.
3. **Whether a `url` may name a host other than `bcat-data-lp1`.**
4. **Splatoon 2.** Its schedule seed is the other title §2.4a was for; it
   has not been tried over the native path. One boot of Splatoon 2 against
   grid0 with the probe on (`stubs.bcat_record_dir`) shows what it asks
   for.

### 2.4a The `.nro` installer — **P3, fallback only**

`switch/bcat-installer` writes a seed straight into a title's BCAT save,
what JKSV does by hand. Since §2.4 works natively, it is only for a console
on a firmware the signature patch does not cover yet. Do not extend it;
fix the patch instead.

---

## 3. Dashboard

### 3.2 Splatfest asset editing — **P2, depends on §2.1**

The Splatfests tab creates, announces, holds and deletes fests. It does not
touch assets: splashart, tee designs, team names and idol dialogue all still
come from files built elsewhere.

The intended end state is doing all of it from the dashboard. §2.1 is the
blocker, not the UI.

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

### 4.6 A console looped publish/revoke on the licence host — **P3, watch**

On 2026-10-09 from 12:24 to 15:55 UTC one console (`did:6265994bb2240805`)
alternated `POST /v2/elicense_archives/publish` and `POST /v2/elicenses/revoke`
on `dragons` about ten times a second (~123,000 requests), starting the moment
it connected and stopping on its own. It has not recurred. GRID0+ answers
publish with an empty `elicense_archives` list and revoke with `{}`
(`internal/stubs/handler.go`, `dragons`); `nim` most likely reads one of those
as unfinished and retries. The paths are in `nim`'s request tables
(`/{version}/elicenses/revoke` at 0x71001b7c98, a Ghidra project is now at
`~/switchnet-re/ghidra_projects/nim_main`), but the response it expects means
tracing nim's generic HTTP layer, and the rights layer belongs to the separate
eShop module (Phase 3). The request side is now read: the revoke request
class's body builder (FUN_7100122a00, vtable 0x7100223fb0) sends
`{"elicense_ids": [...]}`, 16-byte licence ids, so nim revokes licences it
holds locally; its URL builder is FUN_7100122920. Its response slot
(0x7100016ec0) just returns 0, so the revoke answer's body is never read and
`{}` is not the problem. The likelier trigger is publish: GRID0+ returns no
archives, nim revokes what the server did not return, then publishes again.
Answering publish properly means echoing real licence archives, which is the
rights layer Phase 3 owns. If it recurs, note what the console was doing at
12:24 (it had just signed in and opened Splatoon 3's BCAT list).

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

Write findings up under `docs/CAPTURES/` using the template, redacted,
reconcile the implementation, and **delete the `TODO` comment in the same
commit** so the source and the notes cannot disagree.

---

## 5. Housekeeping

Nothing open.

---
