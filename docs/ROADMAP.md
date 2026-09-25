# Roadmap

Five phases. Each one is only worth starting when the previous one has been
confirmed working on real hardware, because otherwise a failure has two possible
causes and no way to tell them apart.

Phase 2 was built against that rule rather than in obedience to it: the
certificate-trust question that gates hardware testing was open, so every step
was chosen to be verifiable without a console. Both have since reached
hardware: phase 1 works, and phase 2 gets as far as Splatoon 3's hall.

| Phase | Scope | State |
| --- | --- | --- |
| [1](#phase-1--accounts-and-the-stock-friends-list) | accounts, friend codes, friends, presence | **running on a real console**; received-requests view still shows 2124-0104 |
| [2](#phase-2--splatoon-3-npln--dashboard) | NPLN game server for Splatoon 3, web dashboard | **running on a real console up to the hall**, which fails ([`TODO.md`](../TODO.md) §4.2) |
| [3](#phase-3--the-eshop-a-separate-module) | eShop, served from local NSP dumps | **lives outside this repository** as a separate, optional module — not started |
| [4](#phase-4--cloud-save-backup-olsc) | per-game save backup (OLSC) | code complete on a defensible-but-unconfirmed shape, **not yet tried on a console** |
| [5](#phase-5--account-system-refactor--a-discord-bot) | account creation/linking refactor, a Discord bot as the primary registration and per-user control surface | **planned, not started** |

Phase 3 is scoped differently from 1 and 2: it is not going into `internal/`
here at all. See its section below for why and what that means in practice.
Phase 4 stays in this repository (unlike phase 3, it needs no game dumps and
carries none of phase 3's distribution-of-copyrighted-content exposure), but
depends on phase 1 the same way phase 3 does, for the same reason. Phase 5
also depends on phase 1 — it replaces part of the account-creation path phase
1 built — but does not depend on phase 1 being hardware-verified first the
way 3 and 4 do: nothing about it is Splatoon- or NPLN-specific, and it can be
designed (though not safely *finished*) in parallel.

---

## Phase 1 — accounts and the stock friends list

**Done in code.** The auth chain, the friends system, the stub and NAT-check
support servers, the Switch-side config generation and the deployment docs are
all implemented and tested: 166 tests, and the whole chain verified running
against a live PostgreSQL and Redis over real HTTP.

**On hardware (2026-09-20 to 09-23):** sign-in, Nintendo Account linking and the
friend list work in the stock applet. Fixed on the way: the NA-link blob header
(2124-0292), penne notification tokens, and nginx asking the friends host for a
client certificate (2121-1058). Still open: the received-requests view shows
2124-0104, traced to the client side and parked. The exit criterion — two
consoles seeing each other's online / "Playing X" status — still needs the
second console.

The remaining BAAS `2.0.0` guesses are listed in
[`CAPTURES/README.md`](CAPTURES/README.md); the friend list shape is now
confirmed by the applet rendering it.

**Console-side setup.** What this deployment actually runs is the SwitchNet
Toolbox homebrew: Atmosphère's hosts file plus the public
`disable_ca_verification` patches, on an emuMMC that never talks to Nintendo
(see [`../switch/README.md`](../switch/README.md)). The routes below are the
narrower alternatives, still worth having for a console that does go back.

Redirection can be solved off the console with DNS
(the generated dnsmasq/unbound configuration, added to a resolver the operator
already runs), so nothing goes on the SD card for that half. Certificate trust does need the console, and the
preferred route — extending the console's own CA store via a LayeredFS override of
title `0100000000000800` — has one unverified step: whether Atmosphère's `fs.mitm`
serves that file to the `ssl` sysmodule at all. `ShouldMitmProgramId` MITMs
applications plus `ns`, `settings` and `sdb`/`glue`, not arbitrary system modules,
so it depends on how `ssl` reads the archive.
[`../switch/README.md`](../switch/README.md#first-find-out-whether-atmosphère-will-serve-the-file-at-all)
has a five-minute test that settles it.

**If that test says no**, the work item is a small Atmosphère MITM sysmodule that
intercepts `ssl`/`ssl:s` and calls `ImportServerPki` with SwitchNet's CA on each
context created. That keeps both properties that matter — not gated on a firmware
build id, and adding trust rather than disabling verification — at the cost of
being code rather than a data file. Prelude shipped something similar
(`network_mitm`) and removed it after it stopped loading on current firmware,
which is a reason to write it carefully rather than a reason the approach is wrong.

**The cost of the Toolbox route**, which this section originally ruled out:
the patch silently does nothing when the firmware build id does not match, and
it turns off certificate verification system-wide. On an emuMMC that only ever
talks to SwitchNet the second cost does not reach Nintendo traffic; the first
means a firmware update needs a new patch before anything works.

---

## Phase 2 — Splatoon 3 (NPLN) + dashboard

**Implemented, and running on a real console up to Splatoon 3's hall**, where
it stops with a communication error ([`../TODO.md`](../TODO.md) §4.2).

Splatoon 3 does not use NEX/PRUDP. It uses **NPLN**: gRPC over HTTP/2 with
protobuf payloads, one tenant per title. The Splatoon 3 tenant is `dce9377b`, at
`t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net`.

That is good news. Compared to NEX's custom binary protocol, NPLN is a standard
transport with mature tooling, and **Kinnay has published the decompiled protobuf
definitions** at [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols)
— covering more than expected, including Splatoon 3's own `toyohr/v1` with
`fest.proto`.

> **This section was written before any of it was built, and four things in it
> turned out to be wrong.** They are corrected in place below, and
> [`PHASE2_PLAN.md`](PHASE2_PLAN.md) is the record of how each was found.
> [`../TODO.md`](../TODO.md) is the live work list — this is the shape of the
> phase, not its state.
>
> In short: **gRPC is not the first thing the game talks to**; three hosts were
> misfiled; **BCAT was missing from the plan entirely** and is what makes a
> Splatfest actually appear; and certificate trust for this title is a separate
> problem from Phase 1's, because Splatoon 3 pins its own.

### 2.1 Protobuf pipeline

**Done.** `scripts/fetch-proto.sh` clones the upstream definitions from a pinned
commit; `scripts/gen-proto.sh` runs `buf` to generate Go server stubs into
`proto/` — 77 files.

**Do not hand-transcribe the `.proto` files.** Always regenerate from upstream, so
an update is a one-command refresh rather than a diffing exercise. Only the
generated Go lands in git, so `go build` needs neither the network nor a protobuf
toolchain.

This step was genuinely independent of phase 1's hardware verification, which is
why it was first.

### 2.1a The JSON front end comes FIRST

**Splatoon 3 does not open with gRPC.** It speaks ordinary JSON over HTTPS to
`gw.hac.lp1.vermillion…` (device init) and `val.hac.lp1.penne…` (login tickets,
and the FQDN of a `fro-*.hac.lp1.penne…` frontline host), and only *then* opens
the gRPC channel to the tenant. Those four hosts were originally filed as
ordinary stubs; they are `ServiceNPLNRest` now.

They need different nginx from the gateway — `proxy_pass` and HTTP/1.1, against
`grpc_pass` and HTTP/2. Routing either through the other's block completes the
TLS handshake and then fails with nothing in either log.

The frontline connection is a long-lived stream the console holds open; closing
it is an error on the console, not a clean end.

**Built** (`internal/nplnrest`, on `npln.rest_listen`, a second listener
alongside the gRPC gateway in `cmd/npln`). Penne's two endpoints answer
against a real field-level spec; Vermillion's two answer honestly-empty JSON,
since the wiki names them but gives no response field table. Every other
Vermillion endpoint, and `god` (Penne provisioning), are still unanswered.
Three real open questions remain, each in `docs/QUESTIONS.md` — see
[`../TODO.md`](../TODO.md) §1.3 for what they are and the assumption each was
built on.

### 2.2 Gateway and core services

**Done.** `cmd/npln` + `internal/npln`. gRPC middleware reads the
`npln-tenant-id`, `authorization` and `uid` metadata fields and rejects malformed
metadata the way Nintendo's server does — status 12 (`Unimplemented`) on
production, 14 (`Unavailable`) on development.

> **Correction.** This section had the gateway speaking gRPC from the first
> request. **It does not.** The game first talks JSON over HTTPS to two host
> families and holds a third connection open, and only then opens the gRPC
> channel. Building the gateway first meant building the second half of the
> handshake and never reaching it — see §2.7.

| Service | Note | Built? |
| --- | --- | --- |
| `nn.npln.auth.v1.Auth` | exchanges the BAAS id token for an NPLN session. Verify it against `/1.0.0/certificates` — that is the seam Phase 1 built | yes |
| `nn.npln.maintenance.v1.MaintenanceScheduleService` | always "no maintenance" | yes |
| `nn.npln.matchmaking.v1.*` | see §2.3 | yes |
| `nn.npln.friends.v1.Friends`, `PresenceService` | the console calls these right after boot, per §2.5 | yes — `internal/npln/friends.go`, `presence_service.go` |
| `nn.npln.messaging.v1.Messaging` | **correction, 2026-09-21**: a decompile found zero references to any of its three RPCs anywhere in Splatoon 3 v11.3.0 — the earlier "opens the invite mailbox early in boot" line here was an assumption, not a confirmed one. Splatoon 3's real in-lobby messaging is the separate, toyohr-namespaced `LobbyMessaging` instead (already implemented, see §2.4). This package is a real, kept reimplementation for whichever other NPLN title does call it — just not this one | implemented but likely unused by Splatoon 3 — `internal/npln/messaging.go`, over `internal/messaging`'s in-memory mailbox |
| `nn.npln.gamesync.v1.Gamesync` | the Firestore-shaped document/watch service for lobby and match state, on its own port (7575) | yes, except `CreateRound` and `LazyWriteDocuments` (the latter confirmed absent from the binary 2026-09-21 — `WriteDocuments` is the variant actually used) — `internal/gamesync`; see `../TODO.md` §1.5 for where it turned out to actually be reached from |
| `nn.npln.hydro.v1.Datastore`, `leaderboard` | start with the minimum that does not crash the client | no |
| `nn.npln.ikaros.v1.CompetitionService`, `nn.npln.timber.v1.*` | confirmed absent from Splatoon 3 v11.3.0 entirely (2026-09-21 decompile) — no RPC of either registered | no

**NPLN access tokens use `ES256`, not `RS256`** — the only place in the stack that
does, and `internal/jwt` has a separate EC path for it. Right about this.

### 2.3 Matchmaking and sessions

**Built** (`internal/npln/matchmaking.go`, with `matchmaking_test.go` covering
the queue and session formation): a FIFO queue per mode, in memory. On a match,
form a session naming a host and hand it to every player on their ticket
stream. `AllocateIceServerSet` is built too.

**`players_per_match` defaults to 2 rather than 8.** On a private network of a
handful of consoles, waiting for a faithful 4v4 means never playing.
`npln.mode_players` is how an operator with enough consoles asks for the real
sizes — and the names are not hardcoded, because nothing public documents the
strings the game puts in `matchmaking_config`. Every unknown mode is logged
once, and that log is how they get discovered.

**Built: `SyncGameSession`.** The host publishes its listening port
(`internal/matchmaking`'s `Session.HostPort`, set via `Queue.SetHostPort`);
any matched player, host or joiner, may read the session back with
`KeepAliveOnly: true`, but only the host may actually change it — a
non-host attempting to is `PermissionDenied`, and a stranger gets the same
`NotFound` `GetGameSession`/`JoinGameSession` already give. Everything else
`GameSession` carries (participant counts, state, who the host is) stays
server-authoritative; a client-supplied value for those is ignored, the
same reasoning `AllocateIceServerSet`'s doc comment gives for ignoring the
request's own tenant and user fields.

**Built: match history.** Every formed session — filled on its own or force-
started by an operator — is recorded to `npln_matches` (`Server.recordMatch`
in `matchmaking.go` and `admin.go`), for the dashboard's recent-matches list.
See §2.6.

**NAT traversal for Splatoon 3 is ICE (STUN/TURN), not the Pia NAT-check pair** —
so `natcheck` is not involved in this title, and coturn is a new deployment
dependency. `AllocateIceServerSet` serves `npln.ice_servers` **verbatim**: a
server behind NAT cannot discover its own reachable address, and the addresses
have to be reachable *from the console*. This is the most common cause of "the
lobby fills but the match never starts", and from the server side it is
indistinguishable from a matchmaking bug. `switchnetctl check` warns when the list
is empty; consoles on one LAN work without it.

Note that gameplay is **peer-to-peer**: once a match starts the consoles talk
directly and SwitchNet is out of the path, so the session names a *host player*,
not a station address. `AllocateIceServerSet` reads `npln.ice_servers`, which is
empty by default — consoles on one LAN connect directly, which is the common
case for a private server.

The match log (`npln_matches`) is built — see the "Built: match history" note
above and §2.6.

### 2.4 Splatoon 3 services (`nn.npln.toyohr.v1.*`)

**Every service is registered, including the ones with no handlers.** That is the
mechanism this section's advice depends on: an RPC on a registered service answers
`Unimplemented` and the gateway logs it once by name, whereas an RPC on an
unregistered service fails identically with nothing in the log.

**All of them are built to some degree**; [`../TODO.md`](../TODO.md) §1.4 has
the current state of each, and `internal/toyohr/README.md` the reasoning.
In short: Splatfests end to end (including `SelectFestBreakingNews`,
missing from the proto); generated Versus/Season rotations and a real Salmon
Run rotation shaped like the working Nextendo captures; per-player cloud save
storage; `GameRecord/InitializeAttributes` and the tag calls; lobby
messaging; empty-but-valid Canola/Locker/CoopScenario/Replay reads.

See [`../TODO.md`](../TODO.md) §1.4 for the file-by-file detail and what each
remaining `TODO(capture-needed)` would settle. Log every unimplemented RPC
with a hexdump — that log is the to-do list.

> **Correction.** This section filed Splatfests under `FestService` as if the whole
> feature were gRPC. **It is not.** The schedule carries only a fest's mechanics;
> every custom asset lives in a BCAT pack the console fetches separately — see
> §2.8.

`FestService` turned out to be the one with a dependency outside NPLN entirely,
which is 2.4a.

### 2.4a BCAT — how a Splatfest actually appears

**Missing from this plan as first written, and it is half the feature.**

`FestService` announces only a fest's *mechanics*: which pack to load, its
revision, the phase times. Every piece of content — team names, splashart, tee
designs, idol dialogue — lives in a **BCAT** pack the console fetches
separately, encrypted with `nisasyst` (AES-128-CBC, IV derived from a CRC32 of
the resource path seeding `sead::Random`).

The announced id, the resource name, the revision and the decryption keys must
all agree, or the game reports `BcatInvalid` — which reaches the player as a
generic communication error. And every fest timestamp must land on a two-hour
boundary or the game demands its verdict instead of opening.

`internal/bcat` builds and encrypts the packs, verified byte-identical against
real ones. What it cannot do is **serve** them: the `list` response body is
undocumented and whether the console verifies the container signature has never
been established. §2.4 of [`../TODO.md`](../TODO.md) has the experiment that
would settle it, and the probe for stage 1 is built.

### 2.5 Friends integration

**Built** (`internal/npln/friends.go`, `presence_service.go`, backed by
`internal/presence` — the TTL and visibility rules `internal/baas`'s own HTTP
presence endpoints were refactored onto, for exactly this section's reason).
Game-reported presence over `KeepAlive`'s `UpdatePresence` is carried
through to friends as `Presence.Attributes` (`presence_attributes.go`), which
is what shows a friend as in a room in Splatoon 3's own list — see
`docs/CAPTURES/README.md`.

**One friend graph, not two.** NPLN has its own friends layer, but it must read
the *same* friendships as BAAS. `internal/npln/friends.go` and `internal/toyohr`
both go through `internal/db` — the same rows. **Do not create a second graph.**

`Friends.ListFriendUsers` and `PresenceService` are both implemented. Two
details from
[the `splatoon-3` write-up](https://github.com/n-popescu/splatoon-3/blob/main/docs/FRIENDS.md)
that they had to get right:

- **`FriendUser.Relationship` must set `presence_deliverable` and
  `presence_receivable`.** If they are false the client will not show a friend's
  presence and will not publish its own — a friend list that looks perfectly
  healthy while everybody is permanently offline.
- **Streams must stay warm.** A stream that goes quiet is treated as dead and the
  friend list stops updating with nothing in the log. `SubscribeMaintenanceSchedules`
  and `RecvMessage` both carry a `KeepAlive` in their own oneof, which is the
  protocol saying so outright, and both are implemented that way. The frontline
  connection in §2.7 is the same rule one layer lower.

### 2.6 Dashboard

**Done.** `cmd/dashboard` plus a small frontend: players with resolved presence,
"who's around" fed by `presencebus` over **SSE** rather than polling, a match
list, the Splatfest controls, a per-player detail view, and the BCAT delivery
cache — including serving it to the console-side installer.

SSE rather than WebSockets: the traffic is one-way, it reconnects on its own, and
it survives an ordinary HTTP proxy with no upgrade handling.

Built, plus a Splatfest section that schedules, announces, holds and deletes
fests, live matchmaking counts (the "in matchmaking" overview tile and the
Matchmaking tab's queue/live-match panels, both fed from `cmd/npln`'s admin
API — the same cross-process wiring `redis.url` above is for, just over
HTTP rather than the presence bus), and a recent match list
(`npln_matches`, written by `internal/npln/matchmaking.go` and
`admin.go`'s `recordMatch`, read by `/api/matches`).

**Switch `redis.url` on before the dashboard runs as a separate process** — the
in-process bus delivers nothing across a process boundary, silently. `cmd/npln`
and `cmd/dashboard` both warn when it is unset, and the matchmaking tile reports
*no data* rather than a zero that would read as "nobody is queuing".

### 2.7 The JSON front end — what this section missed entirely

> **Correction.** Three hosts were listed in `internal/nintendo/hosts.go` as push
> notifications and telemetry. They are the front end the game contacts **first**.

| Host | What it does |
| --- | --- |
| `gw.hac.lp1.vermillion.srv.nintendo.net` | device initialisation, account config |
| `val.hac.lp1.penne.srv.nintendo.net` | login tickets, and the frontline FQDN |
| `fro-N.hac.lp1.penne.srv.nintendo.net` | a persistent connection the console holds open |

`internal/nplnrest` serves them. It needs `proxy_pass` over HTTP/1.1, **not**
`grpc_pass` over HTTP/2 — crossing the two completes the TLS handshake and then
fails with nothing in either log.

That reclassification also answered an open question recorded against
`vermillion`: a 2026-08-27 capture showed 16.5 KB exchanged there mid-flow, which
is not a background contact. It is a device-initialisation handshake that expects
real answers, which is why a stub did not satisfy it. And `fro-N` is a **family**,
not one host — a console handed a frontline FQDN it cannot reach fails after the
handshake looks fine.

**Every body on all three is undocumented.** SwitchNet answers as little as
possible rather than inventing a Nintendo internal, and `npln.rest_record_dir`
writes a hexdumped transcript of one boot — which is the cheapest capture in the
project and settles all three at once.

### 2.8 BCAT — the other half of a custom Splatfest

> **Correction.** This section did not mention BCAT, and custom Splatfests depend
> on it.

A Splatfest exists in **two places that must agree**, or the game raises
`BcatInvalid`:

```
NPLN schedule        names a fest, its resource, and a 40-hex revision
BCAT delivery cache  holds packs whose own metadata names the fest it has
GetFestDecryptionKey hands over keys that must actually open those packs
```

`internal/bcat` builds the packs and the delivery cache; `switchnetctl fest build`
derives all four values from one build so they cannot drift; `switch/bcat-installer`
puts the cache on a console.

**SwitchNet does not serve BCAT**, and the reason is narrower than it looks: the
`list` response body is undocumented, not that the container's signature could not
be produced. `internal/bcat.Probe` is the server for the experiment that would
settle it — see [`../TODO.md`](../TODO.md) §2.4.

**SwitchNet does not convert textures either**, deliberately: a BFRES texture in
the wrong compression or swizzle *loads and renders as garbage*, on a console,
with nothing on the server to look at. `bcat.converter` is a command the operator
points at their own tool, and a source asset with no converter configured is a
build error rather than a PNG packed into the archive.

### 2.9 Certificate trust is a separate problem from phase 1's

> **Correction.** Phase 1's route extends the *console's* CA store. That does
> nothing here: Splatoon 3 carries its own statically linked TLS stack and pins
> its own certificates.

Solved on hardware on 2026-09-23 with four patches, not one — the game also
pins the server key, sets its own verify option and installs a separate
verify callback on the gRPC channel; see
[`switch/README.md` part 3](../switch/README.md#part-3-splatoon-3-pins-its-own-certificates).

The failure signature is worth memorising, because it looks identical to a dozen
unrelated bugs: the TLS handshake **completes**, and the game then abandons the
call before sending a single HTTP/2 HEADERS frame. A connection in the log, and no
request.

[`switch/README.md` part 3](../switch/README.md#part-3-splatoon-3-pins-its-own-certificates)
has the route and its two costs — it *disables* verification rather than *adding*
trust, and it is gated on the **game** version, so a title update stops it
applying silently.

### What is left

[`../TODO.md`](../TODO.md) is the live list. The current blocker is the hall
(§4.2); after that, native BCAT serving (§2.4) and fest asset conversion
(§2.1) are the large pieces left.

## Phase 3 — the eShop, a separate module

Reimplement the Nintendo eShop, initially serving `.NSP` dumps of games the
operator owns, taken from their own console. With a wallet the operator controls,
so balance is whatever they say it is.

**Prerequisite: Phase 1 must be working on hardware**, because the eShop depends
on the account layer for identity, and it additionally needs a Nintendo Account
server that Phase 1 does not provide (see [3.1](#31-nintendo-account-prerequisite)).

### This phase lives outside the SwitchNet repository

Everything below describes what Phase 3 needs, not code that belongs in
`internal/` here. The decision, made 2026-09-20: the eShop is developed as a
separate, optional module — its own repository/package, addable to a
deployment later rather than bundled into core SwitchNet.

Why it's a separate module and not another `internal/` package:

- **It's a different kind of system.** Phases 1 and 2 reimplement protocol —
  requests and responses, shapes settled by capture. The eShop additionally
  needs a storefront web app, a wallet/ledger, and rights/ticket bookkeeping
  tied to real game dumps the operator owns. That's a genuinely different
  engineering surface, not more of the same kind of work.
- **It's a different exposure profile.** Phase 1/2 talk protocol with
  consoles the operator already owns. Phase 3 additionally handles real NSP
  content, however narrowly scoped (see [3.8](#38-scope-note)) — an operator
  who only wants accounts, friends and NPLN gets none of that surface area or
  its considerations by not installing the module.
- **It genuinely is separable.** Nothing in Phase 3 changes how Phase 1/2
  work; it only *depends on* them (identity, device auth — see
  [3.1](#31-nintendo-account-prerequisite)). A dependency that only points one
  way is exactly what a module boundary is for.

What this means in practice, for now: this document keeps describing Phase 3's
scope and open questions, because that work is real and worth tracking — it
just won't land as a PR against this repository. When Phase 3 work actually
starts, expect a separate repository that imports or is added alongside this
one (config wiring, `internal/nintendo/hosts.go`'s host table, and the
account/dauth layer are the integration points), not a new `internal/eshop`
here.

### The single most important thing to know

**The Switch eShop is a website.** When the eShop applet opens it loads
`https://bugyo.hac.lp1.eshop.nintendo.net/ashigaru/` and renders it in the
console's WebKit view. The applet injects a `window.nx` bridge that the page calls
to reach system functions.

So the storefront is a web app we write, not a native UI to reverse-engineer. That
changes the shape of this phase completely, and it is why "every functionality
should work as on stock" is a reasonable goal rather than a fantasy.

Reference:
[eShop Applet](https://github.com/kinnay/NintendoClients/wiki/eShop-Applet-(Switch)).

The bridge, from that page — the useful half:

| Function | What it means for us |
| --- | --- |
| `nx.shop.requestNaAuthToken` | the page asks the console for a Nintendo Account token |
| `nx.shop.requestDeviceRegistrationInfo` | device/account registration state |
| `nx.shop.requestRegisterDeviceAccount` | register this console against the account |
| `nx.shop.requestSyncRights` | **this is the install trigger** — see below |
| `nx.shop.requestEdgeToken` | the CDN token for content downloads |
| `nx.shop.getApplicationContentMetaStatusArray` | what is already installed |
| `nx.shop.prepareSpaceSizes`, `getTotalSizeNand`, `getTotalSizeSd` | free-space checks |
| `nx.shop.requestLinkDevice` / `requestUnlinkDevice` | primary-console handling |

Note what is **absent**: there is no "install this title" call. The page's job
ends at commerce and rights. `requestSyncRights` makes the console fetch its
entitlements, and then the system's own `nim` downloads and installs from `atum`.
That is a large amount of work we do not have to do — but it also means a purchase
that does not result in an install is almost always a rights problem, not a
storefront problem.

### 3.1 Nintendo Account (prerequisite)

The eShop signs in with a Nintendo Account, which Phase 1 does not implement.
`accounts.nintendo.com` needs, at minimum:

- `GET /connect/1.0.0/authorize` — the OAuth-style authorize page
- `POST /connect/1.0.0/api/token` — token issuance
- `GET /2.0.0/users/me` — the profile the eShop displays

Reference:
[Account Server (Switch)](https://github.com/kinnay/NintendoClients/wiki/Account-Server-(Switch)).

Design decision to make early: **link the Nintendo Account one-to-one with the
BAAS user** created in Phase 1, rather than introducing a second account concept.
Phase 1 already records a Nintendo Account id in the user's `self` extras for
exactly this.

### 3.2 Commerce and the wallet

The balance and the catalogue are entirely server-side, so they are ours to
define. "Infinite money" is a ledger we write, not a bypass:

- an `eshop_wallet` table per account, with a balance and a transaction log;
- an admin endpoint (and a dashboard button) to top it up by any amount;
- a config switch for an *unlimited* wallet, where a purchase always succeeds and
  the balance never moves.

Keep the transaction log even with an unlimited wallet. "What did I buy and when"
is the useful half, and it is what the eShop's purchase history screen reads.

The commerce APIs live on `ecs-lp1.hac.shop.nintendo.net` and
`ias-lp1.hac.shop.nintendo.net`. These are **the least documented part of this
phase** — the wiki has one endpoint name
(`/ecs/services/rest/AccountGetDynamicETickets`) and nothing else. Expect to
capture, and expect this to be where the time goes.

### 3.3 Rights and tickets — the real technical constraint

This is the part that decides whether Phase 3 works at all, so it is worth being
precise.

A title installs only if the console accepts its ticket. On a stock console the
ticket must be signed by Nintendo's RSA key. Two paths, and both need
**sigpatches** (ES/FS patches) on the console — which a console running SwitchNet
already needs:

| NSP kind | Contains | Works because |
| --- | --- | --- |
| **Standard crypto** — a gamecard dump, or `nxdumptool` with console-specific data removed | no personalised ticket; the title key is in the NCA header under a fixed key-area key | nothing console-specific to reject |
| **Personalised ticket** — the base ticket from a digital purchase | the original Nintendo-signed ticket | the signature is genuine |

Prefer standard-crypto dumps: they carry nothing tied to the operator's console,
so they are also the safer thing to have sitting on a server.

`dragons.hac.lp1.dragons.nintendo.net` is the eLicense server that issues and
tracks rights. Phase 1 currently **stubs it with `{}`**, which is enough for aauth
to proceed and not enough for Phase 3. The endpoints that matter:

- `POST /<v>/rights/available_elicenses` — what this account may install
- `POST /<v>/rights/publish_elicenses` — grant a right after purchase
- `POST /<v>/elicenses/exercise` — the console claiming one
- `POST /<v>/contents_authorization_token/issue` — the token `aauth` then accepts
- `POST /<v>/edge_token/issue` — the CDN token for `c2` paths on atum

Reference:
[Dragons Servers](https://github.com/kinnay/NintendoClients/wiki/Dragons-Servers).

### 3.4 Content delivery

`atum.hac.lp1.d4c.nintendo.net` serves the actual bytes, addressed by content id,
with an edge token in the `X-Nintendo-DenebEdgeToken` header:

| Path | Serves |
| --- | --- |
| `/c1/a/d/<title id>[/<version>]` | the control NCA — icon and name |
| `/c1/t/a/<title id>/<version>` | the NCA containing the CNMT |
| `/c1/<title id>/c/a/<content id>` | the CNMT NCA |
| `/c1/<title id>/c/c/<content id>` | an NCA of game content |
| `/c1/<title id>/c/c/<content id>/d` | a JSON object of content hashes |

`c1` wants a dauth edge token; `c2` wants a dragons one.

**An NSP maps onto this cleanly.** An NSP is a PFS0 archive whose members are
`<content id>.nca` plus a `.tik` and `.cert`. So an ingest step:

1. read the PFS0 directory;
2. index each NCA by the content id in its filename — that is exactly the key
   atum addresses;
3. parse the CNMT NCA for the title id, version, and content list;
4. read the control NCA for the name and icon, which the storefront and IDBE need;
5. serve the members byte-for-byte on range requests.

Serving NCAs unmodified is the whole trick: no re-encryption, no key handling, no
re-signing. `atum` becomes a content-addressed file server over the operator's
NSP library. This is the most tractable part of Phase 3 and a sensible place to
start.

Reference: [Atum Server](https://github.com/kinnay/NintendoClients/wiki/Atum-Server),
and switchbrew on [NCA](https://switchbrew.org/wiki/NCA) and
[CNMT](https://switchbrew.org/wiki/CNMT).

### 3.5 The supporting servers

| Host | What | Effort |
| --- | --- | --- |
| `tagaya.hac.lp1.eshop.nintendo.net` | `GET /tagaya/hac_versionlist` — latest version per title. Drives the update badge | small; one JSON document generated from the NSP index |
| `idbe-hac.cdn.nintendo.net` | icon data, per title | small; extracted from the control NCA at ingest |
| `superfly`, `pushmo`, `beach`, `pearljam` | metadata and recommendations | can stay stubbed for a long time |
| `aqua.hac.lp1.d4c.nintendo.net` | system update requirements | keep stubbed; see the warning below |
| `hac.lp1.savanna.srv.nintendo.net` | the newer eShop applet | check which one the operator's firmware uses **before** building against `bugyo` |

**`bugyo` requires the "SwitchShop" client certificate.** Like every client cert
in this stack, SwitchNet will not verify it — but nginx must be configured
`optional_no_ca` and not `on`, or the connection is refused before it becomes a
request.

**Know what `sun`, `atumn` and `aqua` are before touching this.** They are
system updates — not to be confused with `atum` (no `n`), which is game
content and a different host. `nim` writes a persistent flag when a system
update check fails, and that flag survives switching DNS back to real
Nintendo — expensive to hit on a console that moves back and forth between
SwitchNet and real Nintendo. `internal/nintendo/hosts.go` currently
null-routes them instead of leaving them passthrough, because this operator's
emuMMC is permanently and exclusively SwitchNet's and never switches back —
see the comment there and `TestSystemUpdateHostsAreNullRouted` for the
reasoning. If you are building this for a console that *does* move back and
forth, put these back to `ServicePassthrough` first, or you will hit that
persistent flag.

### 3.6 Suggested order

Each step is useful on its own, which matters because 3.2 is the one with real
unknowns.

1. **NSP ingest and `atum`.** A content-addressed server over the NSP library,
   plus `tagaya` and IDBE from the same index. Testable entirely from `curl` with
   no console.
2. **`dragons`** for real: a rights table, `available_elicenses`,
   `publish_elicenses`, `exercise`, and the contents-authorization token that
   `aauth` already accepts.
3. **Verify an install with rights granted out of band** — no storefront yet.
   Grant a right by hand, trigger a rights sync on the console, and watch `nim`
   download from `atum`. **This is the milestone that proves Phase 3 is possible**,
   and it comes before any UI work.
4. **Nintendo Account**, enough for the eShop to sign in.
5. **The storefront web app** plus the ECS/IAS commerce API and the wallet.
6. **The rest of stock behaviour**: DLC and updates (just titles), pre-orders,
   wishlist, purchase history, redownload, news.

### 3.7 What will not work like stock, and should be said out loud

- **Catalogue metadata is ours.** Nintendo's descriptions, screenshots, ratings
  and trailers are not in an NSP. The storefront shows the name and icon from the
  control NCA plus whatever the operator writes.
- **My Nintendo points, gift cards and download codes** have no counterpart
  unless we invent one. Easy to invent; will not match stock.
- **Nintendo Switch Online purchase** is meaningless here — Phase 1 already
  reports every user as entitled.
- **Anything the operator does not own** is not in the library. That is the point.

### 3.8 Scope note

Phase 3 serves games the operator dumped from their own console, to consoles the
operator owns, on the operator's own network. That is the only mode described
here and the only mode the documentation should describe.

It ships no Nintendo content, no keys, and no dumps, and it does not fetch
anything from Nintendo. A deployment reachable by anybody other than the operator
would be a distribution service for copyrighted games, which is a different thing
entirely and not what this is. Keep `baas.allow_registration` off and the firewall
tight.

---

## Phase 4 — cloud save backup (OLSC)

Reimplement per-game save data backup: the OS-level "back up your save data to
the cloud" feature Nintendo ties to a Switch Online subscription. This is
**not** the same thing as any one game's own cloud-save API — Splatoon 3's
`CloudSave.GetSaveRecord` (phase 2, [2.4](#24-splatoon-3-services-nnnplntoyohrv1))
is a completely different, game-specific RPC on a completely different
protocol. OLSC is system-wide: every game with save data can use it, through
one shared system service, independent of NPLN or any other game protocol.

**Prerequisite: Phase 1 must be working on hardware**, same reason as phase 3 —
identity comes from the account layer.

### 4.1 What's confirmed, and how

Found 2026-09-20 while sweeping every system title for Nintendo hostnames
(see [`internal/nintendo/hosts.go`](../internal/nintendo/hosts.go)'s
`%.sata.srv.nintendo.net` entry for the full citation). Title
`010000000000003E` implements `nn::olsc`'s IPC interfaces —
`IOlscServiceForApplication` (16 commands), `IAsyncListData`,
`IAsyncTransferRequest`, `IForbiddenSaveDataIndication` — alongside
`nn::sprofile`'s background-agent interfaces in the same binary. It connects
to `dauth:0` for its own device token, and to `npns:s` directly, which means
push notifications ("your backup finished", plausibly) are a real feature of
this system, not a guess.

**Both real backend hosts, not a guess between them.** Kinnay's public wiki
has a dedicated [OLSC Servers](https://github.com/kinnay/NintendoClients/wiki/OLSC-Servers)
page — the same kind of public source this project already leans on heavily
for BAAS/dAuth/aAuth — documenting `storage.hac.lp1.scsi.srv.nintendo.net`'s
`save_data_archives` API (v2, full field-level shape for the archive and
component-file objects, `start_upload`/`finish_upload`/
`generate_key_seed_package`/`extend_upload_timeout`) and
`permission.lp1.sata.srv.nintendo.net`'s `save_data_sets`/
`generate_key_package` (listed with no method or shape at all). Both are
implemented in `internal/scsi`.

### 4.2 The storage backend decision — made

Everything in phases 1-3 either reimplements a *protocol* (phases 1, 2) or
serves content the operator already owns and dumped themselves (phase 3).
Phase 4 is different in kind: a working implementation means **storing
arbitrary player save data**, indefinitely, for every console in the group —
real storage cost that grows with usage, not a one-time dump.

Decided: **local disk on the box already running SwitchNet.** No new
recurring cost — the operator already pays for this machine regardless.
Configured via `scsi.storage_dir`; `scsi.per_user_quota_bytes` (default
1 GiB) is the hard per-user cap this section originally called for, enforced
in `internal/scsi.LocalDisk.Put` so one console's save data cannot fill the
disk and take the rest of SwitchNet down with it. If disk space specifically
becomes a real constraint, `internal/scsi.BlobStore` is the seam an
object-storage implementation (Azure Blob, Backblaze B2, Cloudflare R2) would
plug into without touching anything else in the package — see
`internal/scsi/README.md`.

### 4.3 What's actually still open

**No capture of real OLSC traffic exists yet.** Most of `internal/scsi` is
built against the wiki's confirmed shape, but four things are this project's
own defensible reading rather than a confirmed one — each marked
`TODO(capture-needed)` at the exact function in
[`internal/scsi/handler.go`](../internal/scsi/handler.go), and ranked by how
blocking each is in
[`docs/CAPTURES/README.md`](CAPTURES/README.md#phase-4--cloud-save-olsc):

1. **`generate_key_seed_package` and `generate_key_package`.** Confirmed to
   exist; not confirmed whether a console cryptographically validates either
   response before trusting a restore. If it does, this is the single most
   likely hard blocker in the whole feature — the same category of unknown
   `nxAccountBlob`'s header prefix turned out to be for phase 1's account
   linking, and a capture is what actually settled that one.
2. **`component_files/create` and `signed_uri`.** Listed on the wiki with no
   method or field shape at all; SwitchNet answers with its own invented
   same-server upload/download endpoint rather than real request signing,
   since it controls both ends of the exchange anyway.
3. **`start_download`/`finish_download`.** No documented shape at all;
   answered as "the same as reading the archive" and "a bare
   acknowledgement" respectively.
4. **Where `application_id` comes from.** `start_upload`'s documented
   request body has no title-id field. Read from a SwitchNet-invented
   `X-SwitchNet-Application-Id` header for now.

### 4.4 Scope note

Same boundary as phase 3's, for the same reason: this backs up save data for
consoles the operator owns, on the operator's own network. It is not a
general-purpose backup service and should never be reachable by anyone else's
console.

---

## Phase 5 — account system refactor + a Discord bot

**Not started. This section is a plan, not a design that has been implemented
or validated against real hardware.** It records a decision to make later,
not a decision already made — everything below is free to change once actual
implementation starts and runs into something this plan did not anticipate.

**Depends on phase 1's code** (the account/login/friend-code layer this
touches), **but not on phase 1's hardware verification** the way phases 3 and
4 do. Nothing here is Splatoon- or NPLN-specific, so the design work — and
probably the bot's non-account commands — can proceed in parallel with
phase 1's hardware test. Actually *changing* how accounts are created and
linked should still wait until phase 1 is confirmed working end-to-end,
since it touches the exact code path
([`internal/baas/nalink.go`](../internal/baas/nalink.go),
[`accounts.go`](../internal/baas/accounts.go)) that test is meant to
validate — breaking it silently while "just adding a bot" would erase the
hardware test's own signal.

### 5.1 What's wrong today, concretely

Two real problems in the current account-creation path, both confirmed by
reading the code rather than assumed:

1. **Linking a Nintendo Account leaves an orphaned "throwaway" user
   behind.** A console's own account-creation flow always calls
   `POST /1.0.0/users` first — `registerUser`
   ([`accounts.go:111`](../internal/baas/accounts.go)) — which has no choice
   but to invent a brand new `db.User` + `db.DeviceAccount` on the spot,
   before anyone has said who the real person is (see nalink.go's own
   package comment, "the disposable identity registerUser had to invent
   before any id token existed"). When that person then links through the
   Nintendo Account page with their real SwitchNet login, `completeLogin`
   ([`accounts.go:414`](../internal/baas/accounts.go)) calls
   `ReassignDeviceAccount` to move the *device account* onto their real,
   persistent `User` — but the disposable `User` row `registerUser` created
   is never touched. It sits in the `users` table forever, with zero device
   accounts pointing at it, showing up in the dashboard's Players list and
   in every user count. This is the "second throwaway account" problem: it
   is not a bug in the sense of broken behavior (login still works
   correctly), it is a bookkeeping leak in a process meant to run once per
   *console*, not stay invisible once per *person*.
2. **There is no operator-facing way to create a person's SwitchNet
   identity that isn't "wait for a console to invent one."** The self-
   registration website (`website.go`'s `CreateUserWithLogin`) already
   creates a `User` with `LoginUsername`/`LoginPasswordHash` and *no* device
   account, lazily minting one on first login
   (`deviceAccountForLogin`, [`accounts.go:319`](../internal/baas/accounts.go))
   — which is most of what a bot-driven `/register` needs, but it is a
   public HTML form today, not something a Discord command can drive
   without scraping its own page.

### 5.2 Discord bot as the primary registration path

`/register` (Discord slash command, one per Discord account):

- Takes the caller's Discord **username** (not display name/nickname — the
  stable, unique one) as the SwitchNet `LoginUsername`.
- Generates an 8-character password from `[a-z0-9A-Z]` server-side (the
  same kind of random-credential generation `newDevicePassword` already does
  for device accounts, just a human-typeable alphabet instead of that
  function's own), shows it to the caller **once**, and never stores it in
  plaintext or logs it — same handling `registerUser`'s response already
  gives a device account password.
- Creates the `User` + `LoginPasswordHash` (bcrypt, matching
  `CreateUserWithLogin`'s existing hashing) the same way the website does
  today, through the same store call — this needs no new account-creation
  logic, only a new caller for the existing one, reachable from the bot
  process rather than only from an HTTP form.
- Records the Discord user ID against the new `User` row (a new
  `DiscordUserID` column, unique, nullable for accounts that never went
  through the bot) so every other command below can resolve "the caller" to
  a SwitchNet user without asking them to re-authenticate per command.

The password the bot hands out is typed into the Switch's own "link a
Nintendo Account" flow exactly the way a website-registered person already
would — `nalink.go`'s login page already checks a SwitchNet
username/password, not a device-account id/password, so nothing about the
console-facing protocol changes; only the source of the credentials does.

**Optional, not required for v1** (the user's own words): letting a person
rotate their password after first use, e.g. `/reset-password`, generating a
new one the same way and invalidating device-account tokens minted before
it the same way `TokensRevokedBefore` already does elsewhere in this
codebase. Worth having, not blocking.

### 5.3 Redesigning the throwaway-account problem

The actual ask: stop counting a console's disposable pre-link identity as a
real user, without breaking the fact that `registerUser` structurally has to
create *something* the instant a console asks for an account, before it can
possibly know who that will turn out to be.

Proposed shape (a plan to validate against the real `Store` interface when
this is implemented, not a final schema):

- A new, separate table — `device_provisioning_identities` or similar —
  holds exactly what a disposable pre-link identity needs to exist at all:
  an id, its one `DeviceAccount`, and nothing else. Not a `User` row, so it
  never appears in `ListUsers`/the dashboard's Players count/anywhere a real
  person is counted.
- `registerUser` creates a row here instead of a `db.User`, the very first
  time a console asks for an account with no prior link.
- `db.User` gains a field naming its **current** provisioning identity (a
  nullable FK) — the row a console's device account is presently attached
  to before or absent a link, exactly one at a time.
- When `completeLogin`'s federation path reassigns a device account onto a
  real person (today's `ReassignDeviceAccount`), the *old* provisioning
  identity that device account used to belong to is hard-deleted in the same
  transaction, once nothing still points at it. "Hard-deleted," per the
  request explicitly — not soft-deleted, not kept for audit: it never held
  anything but a disposable device-account credential nobody needs a record
  of once it is superseded.
- A person can still end up with **no** provisioning identity at all (the
  bot-registered case, same as today's website registration): the FK is
  nullable, and `deviceAccountForLogin`'s existing lazy-mint-on-first-login
  behavior is unaffected either way.

What this does *not* change: the wire protocol, `nalink.go`'s login page, or
`completeLogin`'s password-then-reassign security property (a device account
is only ever reassigned after proving its own password, never on an id
token's claim alone — see that function's own comment). This is a storage
and bookkeeping change, not a protocol change.

### 5.4 Avatars from Discord instead of the static placeholder

Today, an account with no uploaded thumbnail falls back to
`ensureDefaultAvatar` — a generated solid-colour square
([`internal/baas/images.go`](../internal/baas/images.go)). The plan: at
`/register` (and optionally on a later `/avatar` re-sync command), the bot
fetches the caller's Discord avatar, converts it to JPEG, and stores it the
same way an uploaded thumbnail already is — through the existing
`POST /1.0.0/image_upload` shape/`cfg.ImageDir`
([`internal/baas/images.go`](../internal/baas/images.go)) rather than a
second, parallel image store. `cfg.ImageDir` already exists as exactly the
"don't scatter files at repo root" folder the request asks for; nothing new
needs creating there beyond what this endpoint already writes into it. Real
dimensions to resize to (Discord avatars come in several sizes; the
`imageUploadRequest.RawContent` shape this reuses expects a single JPEG) are
an implementation detail to pin down against `images.go`'s own handling when
this is actually built, not decided here.

### 5.5 Other bot commands, grounded in what SwitchNet already stores

Every command below reads or writes data that already exists in `internal/db`
and `internal/baas` — nothing here proposes tracking anything new besides
5.2's `DiscordUserID` link and 5.3's provisioning-identity table:

- **`/fc`** — the caller's own friend code (`db.FriendCode`,
  `internal/baas/friendcodes.go`).
- **`/fc-add <user>`** — send a friend request, accepting either a mention
  of another Discord account that has itself run `/register` (resolved
  through `DiscordUserID`) or a raw friend code
  (`XXXX-XXXX-XXXX-XXXX`) for a SwitchNet account that never touched the
  bot at all — friend codes are already how the stock friends applet adds
  someone, so this is the same lookup, just from a slash command
  (`internal/baas/friends.go`, `FriendRequest`).
- **`/fc-pending`** — list the caller's outgoing and incoming
  `RequestPending` rows (`db.FriendRequest`).
- **`/fc-remove <user>`** — end a friendship or cancel/deny a pending
  request — the same operations the stock applet already exposes, via the
  bot instead.
- **`/whoami`** — the caller's own SwitchNet nickname, friend code, and
  linked-console count (`len(ListDeviceAccounts)`), so a person can check
  what the bot has for them without opening the dashboard.
- **`/online`** — who in the group is currently online/playing
  (`db.Presence`, the same states `internal/presence` already resolves for
  the dashboard's live view) — a Discord-native version of "who's on."
  Consider a `/status` variant scoped to just the caller's own friends list,
  not the whole group, if privacy turns out to matter once real people are
  using this.
- **`/status`** — service health, reusing exactly what the dashboard's
  `/api/health` already reports (`internal/dashboard/servicehealth.go`) —
  which services are up, plus a current online-player count — so an
  operator (or anyone in the Discord) can check "is SwitchNet up" without
  opening the dashboard.
- **`/matches`** *(maybe)* — the caller's own recent match history
  (`db.Match`, already recorded by `internal/npln`'s `recordMatch`) once
  phase 2 is confirmed working — low priority, and only worth it if Splatoon
  3 is actually confirmed on hardware, since the data is otherwise empty.

Commands intentionally NOT proposed: anything that would need inventing a
Nintendo-side value (a fake presence state, a fake match result) or that
duplicates a *destructive* dashboard control (deleting an account, revoking
tokens) without the same care `internal/dashboard`'s own confirm-dialog UX
gives those actions today — if bot-driven account moderation is wanted
later, it needs its own authorization story (who in Discord is allowed to
run it), not assumed here.

### 5.6 Open questions to settle before implementation starts

- **Where does the bot process live?** A new `cmd/discordbot`, using Go the
  same as everything else in this repo (a client library exists,
  `bwmarrin/discordgo`), talking to `internal/baas`/`internal/db` in-process
  the way `cmd/switchnetctl` already does, rather than over HTTP — avoids
  inventing a new admin API surface purely for the bot's own use. This
  matches CLAUDE.md's polyglot-per-layer approach (Go for servers) and phase
  4's precedent (stayed in this repo because it is the operator's own
  infrastructure, not distributed content) more than it matches phase 3's
  "lives outside this repository" reasoning (which was about copyrighted
  game content, not applicable here) — but this is a call to make when the
  work actually starts, not fixed by writing it down now.
- **Discord bot token / server management** is itself operator
  configuration (`config.yaml`, a new `discord:` section) — needs a decision
  on which guild(s)/roles are allowed to run which commands, especially
  anything moderation-adjacent.
- **Exact avatar resize dimensions and format**, deferred to 5.4.
- **Whether `/register` should be rate-limited or invite-gated** the way
  `cfg.AllowRegistration` already gates the website/console path — an open
  Discord server with a working `/register` is a different exposure than a
  private HTML form URL.

### 5.7 Scope note

This is account-layer and operator-tooling work, not game-protocol work — it
does not touch `internal/toyohr`, `internal/npln`, or anything Splatoon-3-
specific, and nothing in it needs a game dump or copyrighted content the way
phase 3 does. It stays in this repository.

---

## Captures

Every phase is limited by what is documented, and the friends and commerce layers
are the least documented parts. [`CAPTURES/README.md`](CAPTURES/README.md) lists
what is still guessed, in priority order; [`PACKET_CAPTURE.md`](PACKET_CAPTURE.md)
covers how to capture it — including a method that needs no console MITM and
carries no ban risk. This deployment's console never runs Atmosphère against
Nintendo, so a capture of Nintendo from it is tcpdump only; request and response
shapes come from RE, GDB on the emuMMC, captures against SwitchNet, and facts
other projects have published.
