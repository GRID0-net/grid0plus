# Roadmap

Three phases. Each one is only worth starting when the previous one has been
confirmed working on real hardware, because otherwise a failure has two possible
causes and no way to tell them apart.

| Phase | Scope | State |
| --- | --- | --- |
| [1](#phase-1--accounts-and-the-stock-friends-list) | accounts, friend codes, friends, presence | code complete, **awaiting hardware verification** |
| [2](#phase-2--splatoon-3-npln--dashboard) | NPLN game server for Splatoon 3, web dashboard | in progress: gateway, matchmaking+ICE, dashboard and BCAT pack-building are built and tested; the JSON front end, most `toyohr` services and BCAT serving are not |
| [3](#phase-3--the-eshop) | eShop, served from local NSP dumps | not started |

---

## Phase 1 — accounts and the stock friends list

**Done in code.** The auth chain, the friends system, the stub and NAT-check
support servers, the Switch-side config generation and the deployment docs are
all implemented and tested: 166 tests, and the whole chain verified running
against a live PostgreSQL and Redis over real HTTP.

**The remaining gate is hardware.** The exit criterion is an operator creating an
account on a real modded Switch, generating a friend code, adding a second Switch,
and seeing accurate online / offline / "Playing X" status in the **stock**
Nintendo friends applet. That cannot be verified without two consoles.

**What is most likely to be wrong when it is tried:** the BAAS `2.0.0` friends
endpoint shapes, which are undocumented and reasoned rather than known. The friend
list response is the one to watch — if it is an envelope rather than a bare array,
the applet comes up empty with nothing in the log.
[`CAPTURES/README.md`](CAPTURES/README.md) lists all ten guesses in priority
order.

**Console-side work still open.** Redirection is solved off the console with DNS
(`internal/dns`, or the generated dnsmasq/unbound configuration), so nothing goes
on the SD card for that half. Certificate trust does need the console, and the
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

**Explicitly not wanted:** a Prelude-style homebrew that rewrites the hosts file
and installs firmware-gated exefs patches. It is fragile in a specific way — the
patch silently does nothing when the build id does not match, and it turns off
certificate verification system-wide — and the DNS route makes most of it
unnecessary.

---

## Phase 2 — Splatoon 3 (NPLN) + dashboard

Splatoon 3 does not use NEX/PRUDP. It uses **NPLN**: gRPC over HTTP/2 with
protobuf payloads, one tenant per title. The Splatoon 3 tenant is `dce9377b`, at
`t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net`.

That is good news. Compared to NEX's custom binary protocol, NPLN is a standard
transport with mature tooling, and **Kinnay has published the decompiled protobuf
definitions** at [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols).

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

`scripts/fetch-proto.sh` clones the upstream definitions; `scripts/gen-proto.sh`
runs `protoc`/`buf` to generate Go server stubs into `proto/`.

**Do not hand-transcribe the `.proto` files.** Always regenerate from upstream, so
an update is a one-command refresh rather than a diffing exercise.

This step is genuinely independent of Phase 1's hardware verification, so it is
the safe thing to do first if Phase 1 is still waiting on consoles.

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

Nothing implements these yet, and all three request/response shapes are
`TODO(capture-needed)` — see [`../TODO.md`](../TODO.md) §1.3.

### 2.2 Gateway and core services

`cmd/npln` + `internal/npln/server.go`. gRPC middleware reads the
`npln-tenant-id`, `authorization` and `uid` metadata fields and rejects malformed
metadata the way Nintendo's server does — status 12 (`Unimplemented`) on
production, 14 (`Unavailable`) on development.

Then, in order of what a session needs:

| Service | Note | Built? |
| --- | --- | --- |
| `nn.npln.auth.v1.Auth` | exchanges the BAAS id token for an NPLN session. Verify it against `/1.0.0/certificates` — that is the seam Phase 1 built | yes |
| `nn.npln.maintenance.v1.MaintenanceScheduleService` | always "no maintenance" | yes |
| `nn.npln.friends.v1.Friends`, `PresenceService` | the console calls these right after boot, per §2.5 | yes — `internal/npln/friends.go`, `presence_service.go` |
| `nn.npln.messaging.v1.Messaging` | opens the invite mailbox, also early in boot | yes — `internal/npln/messaging.go`, over `internal/messaging`'s in-memory mailbox |
| `nn.npln.hydro.v1.Datastore`, `leaderboard` | start with the minimum that does not crash the client | no

**NPLN access tokens use `ES256`, not `RS256`** — the only place in the stack that
does. `internal/jwt` currently only does RS256 and will need an EC path.

### 2.3 Matchmaking and sessions

**Built** (`internal/npln/matchmaking.go`, with `matchmaking_test.go` covering
the queue and session formation): a FIFO queue per mode, in memory. On a match,
form a session naming a host and hand it to every player on their ticket
stream. `AllocateIceServerSet` is built too.

**Not built:** `SyncGameSession` — the host publishing its reachable address
and joiners reading it back, needed once a match actually needs to connect
peer-to-peer rather than just form. Log every match to `npln_matches` for the
dashboard.

**NAT traversal for Splatoon 3 is ICE (STUN/TURN), not the Pia NAT-check pair** —
so `natcheck` is not involved in this title, and a STUN/TURN server (coturn) is a
new deployment dependency. The addresses handed to the console must be reachable
*from the console*: this is the most common cause of "the lobby fills but the
match never starts".

Note that gameplay is **peer-to-peer**: once a match starts the consoles talk
directly and SwitchNet is out of the path, so the session names a *host player*,
not a station address. `AllocateIceServerSet` reads `npln.ice_servers`, which is
empty by default — consoles on one LAN connect directly, which is the common
case for a private server.

The match log is still to do ([`../TODO.md`](../TODO.md) §3.1).

### 2.4 Splatoon 3 services (`nn.npln.toyohr.v1.*`)

Roughly boot-to-match order: `Schedule` (map/mode rotation), `LobbyMessaging`,
`GameRecord`, `CoopScenario` (Salmon Run), `FestService` (Splatfests), then
`Locker`, `CloudSave`, `Replay`, `Canola`, `UserScreening`.

**Built: `FestService` only** (`internal/toyohr`, package doc: "starting with
Splatfests"). `Schedule` and `CloudSave` are called during ordinary boot
queries, ahead of Fest in the sequence a session actually needs — worth
prioritising over the rest of this backlog for that reason.  Everything else
listed above has a generated proto binding under `proto/toyohr/v1` and no
implementation.

Treat this list as a backlog and let capture data drive the order. Log every
unimplemented RPC with a hexdump — that log is the to-do list.

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
Not built: `Presence.Attributes` (undocumented keys) and richer
game-reported presence over `KeepAlive`'s `UpdatePresence` field — see
`docs/CAPTURES/README.md`.

**One friend graph, not two.** NPLN has its own friends layer, but it must read
the *same* friendships as BAAS. Either `internal/npln/friends.go` calls into
`internal/db`'s friendship store directly, or both go through `presencebus` for
the live view. Do not create a second graph.

Two details from
[the `splatoon-3` write-up](https://github.com/n-popescu/splatoon-3/blob/main/docs/FRIENDS.md)
that are easy to miss:

- **`FriendUser.Relationship` must set `presence_deliverable` and
  `presence_receivable`.** If they are false the client will not show a friend's
  presence and will not publish its own — a friend list that looks perfectly
  healthy while everybody is permanently offline.
- **Streams must stay warm.** A stream that goes quiet is treated as dead and the
  friend list stops updating with nothing in the log.

### 2.6 Dashboard

`cmd/dashboard` plus a small frontend. Users list, "online now" fed by
`presencebus` over WebSocket or SSE rather than polling, user detail with friend
list and match history, and a recent match list.

`internal/presencebus` already exists for this. **Switch `redis.url` on before the
dashboard runs as a separate process** — the in-process bus delivers nothing
across a process boundary, silently.

Built, plus a Splatfest section that schedules, announces, holds and deletes
fests. Match history and live matchmaking counts are not wired ([`../TODO.md`](../TODO.md)
§3.1) — and the same cross-process problem applies to them, since the queue lives
in `cmd/npln`.

### 2.7 Certificate trust is a SEPARATE problem here

Phase 1's CA-store work does not cover Splatoon 3. The game carries its own TLS
stack and pins its own certificates independently of the console's trust store,
so redirection succeeding tells you nothing about whether the handshake will.

`kinnay/NPLN-Protocols` ships `generate_patch.py`, which builds an IPS patch
disabling verification in the game's NSO. The operator has accepted a
system-wide patch, so this is a decision already made rather than an open
question — but write down both properties: it *disables* verification rather
than *adding* trust, and it is gated on the game version, so a title update
silently stops it applying.

---

## Phase 3 — the eShop

Reimplement the Nintendo eShop, initially serving `.NSP` dumps of games the
operator owns, taken from their own console. With a wallet the operator controls,
so balance is whatever they say it is.

**Prerequisite: Phase 1 must be working on hardware**, because the eShop depends
on the account layer for identity, and it additionally needs a Nintendo Account
server that Phase 1 does not provide (see [3.1](#31-nintendo-account-prerequisite)).

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

## Captures

Every phase is limited by what is documented, and the friends and commerce layers
are the least documented parts. [`CAPTURES/README.md`](CAPTURES/README.md) lists
what is still guessed, in priority order; [`PACKET_CAPTURE.md`](PACKET_CAPTURE.md)
covers how to capture it — including a method that needs no console MITM and
carries no ban risk.
