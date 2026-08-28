# Phase 2 implementation plan — NPLN, Splatoon 3, and the dashboard

Status: **in progress.** Written 2026-08-28.

| Step | State |
| --- | --- |
| Protobuf pipeline (`make gen-proto`) | done — 77 files from a pinned upstream |
| ES256 in `internal/jwt` | done |
| Host reclassification + nginx generator | done |
| gRPC gateway, and the phase-1 auth seam | done — `internal/npln`, `cmd/npln` |
| Splatfest scheduling | done — `internal/toyohr` |
| Match queue and formation | done — `internal/matchmaking` |
| Matchmaker/GameSession gRPC services | done — `internal/npln/matchmaking.go` |
| BCAT pack building and serving | not started — `internal/bcat` |
| The JSON front end (vermillion, penne, frontline) | done — `internal/nplnrest`, shapes still capture-gated |
| Splatfest entries, results and the rest of toyohr | done — `internal/toyohr` |
| `gamesync` document/watch service | done — `internal/gamesync` |
| Dashboard | not started |

Everything done so far is verifiable without a console, which is the ordering
this plan committed to while the certificate-pinning question is open. The
store work is verified against a real PostgreSQL 16, not only the in-memory
implementation.

This plan revises [`ROADMAP.md`](ROADMAP.md) §2 in four places where the roadmap
turns out to be wrong or incomplete. Those corrections are the reason this
document exists; if you read nothing else, read
[What the roadmap gets wrong](#what-the-roadmap-gets-wrong).

## Where the knowledge came from

Two sources, and they are not equally citable.

[Kinnay's NPLN wiki](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers)
and [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols) are public
reverse-engineering documentation. The protobuf definitions come from there and
are regenerated, never hand-transcribed — same rule as ROADMAP §2.1.

**That upstream covers more than expected, which matters for how this phase is
built.** `latest/proto/` carries not just the generic NPLN services but Splatoon
3's own `toyohr/v1` — including `fest.proto`, with `SelectFestSchedule` and
`GetFestDecryptionKey`, the two RPCs custom Splatfests turn on. So the message
shapes are public, and the implementation is built against them. The study of
Nextendo's server contributes only *behaviour* on top: timing constants, the
pack encryption scheme, and the alignment rule. Nothing structural depends on it.

Cross-checking the two found a place where the public schema is the better
guide. `FestDecryptionKey` defines **seven** key fields — `notice_key`,
`start_key`, `result_winning_team_key`, then `team_alpha_key`, `team_bravo_key`,
`team_charlie_key` — and `GetFestDecryptionKeyRequest` carries a `show_all_keys`
flag. The reference caps its response at three on the grounds that the game never
asks for more, which is plausibly true of the phases it was tested against and
not a property of the protocol. Implement all seven; let the request decide.

The second is a study of NextendoNetwork's own Splatoon 3 server, which the
operator supplied. It is **PolyForm Shield 1.0.0** — source-available, with a
noncompete clause — not an open-source licence, and it is incompatible with this
repository's AGPL-3.0. So it is used for one thing only: learning **facts about
Nintendo's protocol** — field numbers, timing constants, the shape of a
handshake. No code, structure, or prose is carried across. Where a fact below
came from that study it is marked *(observed in reference)*, so a later reader
can tell it apart from something a public document establishes.

That distinction matters practically, not just legally: their measured captures
are **not** in their repository (they load them at runtime from a directory the
operator supplies, and say so). So the reference teaches mechanism, never
Nintendo's measured bytes. Several shapes below stay capture-gated for exactly
that reason.

---

## What the roadmap gets wrong

### 1. Splatoon 3 does not start with gRPC

ROADMAP §2.2 has the gateway speaking gRPC from the first request. It does not.
The game first talks **JSON over HTTPS** to two host families, and only then
opens gRPC *(observed in reference)*:

| Host | What it does |
| --- | --- |
| `gw.hac.lp1.vermillion.srv.nintendo.net` | device initialisation, account config |
| `val.hac.lp1.penne.srv.nintendo.net` | login tickets, and the frontline FQDN |
| `fro-N.hac.lp1.penne.srv.nintendo.net` | a persistent bidirectional connection |

Building the gRPC gateway first means building the second half of the handshake
and never reaching it. The REST front comes first.

### 2. Three hosts in `hosts.go` are misidentified

All three are already in `internal/nintendo/hosts.go`, all three are
`ServiceStubs`, and all three carry comments guessing at a purpose that is not
theirs:

| Host | Current comment | Actually |
| --- | --- | --- |
| `gw.hac.lp1.vermillion…` | "background game registration/telemetry, purpose unconfirmed" | NPLN device init + account config |
| `val.hac.lp1.penne…` | "push notifications (Penne) validation" | NPLN login tickets + frontline FQDN |
| `fro-1.hac.lp1.penne…` | "push notifications (Penne), purpose unconfirmed" | the persistent presence stream |

This **resolves an open question already recorded in this repository.** The
`vermillion` comment notes a 2026-08-27 capture showing 16.5KB exchanged —
"not the negligible amount a background-only contact would suggest" — and flags
it as a candidate cause for the account flow stalling. That is explained: it is
not telemetry, it is a device-initialisation handshake that expects real answers.

Two follow-ons:

- **`fro-N` is a family, not one host.** The reference resolves the frontline to
  `fro-3`; this repository lists only `fro-1`. A console handed a frontline FQDN
  it cannot reach fails after the handshake looks fine.
- **The persistent connection must stay open.** Closing it immediately produces
  a communication error (`2307-2103` on Splatoon 2, `2321-*` on Splatoon 3)
  *(observed in reference)*. It is a long-lived chunked response, not a
  request/response endpoint. This is ROADMAP §2.5's "streams must stay warm",
  one layer lower than the roadmap puts it.

### 3. BCAT is missing entirely, and custom Splatfests depend on it

ROADMAP §2.4 files Splatfests under `nn.npln.toyohr.v1.FestService`, as if the
whole feature were gRPC. It is not. The NPLN schedule carries only the
*mechanics* of a fest — resource names, region codes, timestamps, a pack id and
its fingerprint. **The team names, banners and idol dialogue — every custom
asset — live in a BCAT pack the console fetches separately** *(observed in
reference)*.

So a Splatfest exists in two places that must agree, or the game raises
`BcatInvalid`:

```
NPLN schedule        names a fest, its resource, and a 40-hex revision
BCAT delivery cache  holds packs whose own metadata names the fest it has
GetFestDecryptionKey hands over keys that must actually open those packs
```

The three `bcat-*` hosts are currently `ServiceBlackhole` in `hosts.go`, listed
in `TestBlackholedHostsAreAccountedFor`'s `accountedFor` map. Serving custom
fests means flipping them and amending that test — deliberately, in a commit that
says why.

**Phase 2 therefore needs a BCAT server that the roadmap does not mention.**
This plan adds it as §2.7 work.

### 4. Certificate trust for Splatoon 3 is a different problem from Phase 1's

Phase 1's cert-trust route extends the *console's* CA store. That does nothing
here: Splatoon 3 carries its own statically linked gRPC and TLS stack and pins
its own certificates *(observed in reference)*. A client that satisfies
redirection but not pinning completes the TLS handshake and then abandons the
call before sending a single HTTP/2 HEADERS frame — the game reports a generic
communication error, and nothing useful appears in the server log.

That failure signature is worth memorising, because it is the one the operator
will hit first, and it looks identical to a dozen unrelated bugs.

**This is an operator question, not a code question** (see
[Open questions](#open-questions-for-the-operator)).

---

## Package layout

Flat under `internal/`, matching every existing package — no subpackage trees.

| Package | Responsibility |
| --- | --- |
| `internal/npln` | the gRPC gateway: TLS, HTTP/2, metadata middleware, sessions, auth |
| `internal/nplnrest` | the JSON front that comes first: vermillion, penne, the frontline stream |
| `internal/toyohr` | Splatoon 3's own services: schedule, fest, game records, co-op |
| `internal/gamesync` | the document/watch service, on its own port (7575) |
| `internal/bcat` | pack construction, `nisasyst` encryption, and serving |
| `internal/dashboard` | the web dashboard, fed by `presencebus` |
| `proto/` | generated stubs; `scripts/{fetch,gen}-proto.sh` |

New commands: `cmd/npln`, `cmd/dashboard`. `gamesync` runs inside `cmd/npln` as
a second listener rather than a third process — it shares session state with the
gateway, and splitting it would mean inventing a protocol between them.

Changes to existing packages:

- **`internal/jwt`** — an ES256 path. NPLN access tokens are ES256; every other
  token in the stack is RS256 (ROADMAP §2.2, and it is right about this).
- **`internal/db`** — new tables and store methods, added to the *contract test*
  so both implementations stay honest: `npln_sessions`, `npln_matches`,
  `npln_fests`.
- **`internal/nintendo/hosts.go`** — the corrections in §2 above, plus flipping
  the `bcat-*` hosts when §2.7 lands.
- **`cmd/switchnetctl`** — uncomment `grpcServerBlock`; add the `nplnrest` hosts
  to the ordinary HTTP/1.1 block. `grpc_pass` and `proxy_pass` are not
  interchangeable and routing one through the other fails silently.

**One friend graph.** `internal/toyohr` and `internal/npln` read friendships
through `internal/db`, the same rows BAAS uses. ROADMAP §2.5 is emphatic about
this and it is right.

---

## The first working slice

Ordered so that every step is verifiable *without a console*, because the
console-side blocker (§4 above) is unresolved and may stay unresolved for a while.
Nothing here is gated on it.

1. **Protobuf pipeline.** `scripts/fetch-proto.sh` + `scripts/gen-proto.sh`,
   generating into `proto/`. Independent of everything, including Phase 1's
   hardware gate.
2. **ES256 in `internal/jwt`**, with wire-level tests: decode by hand, assert the
   curve and the absent claims, same as `tokens_test.go` does for RS256.
3. **`internal/npln` gateway skeleton** — TLS, HTTP/2, and the metadata
   middleware reading `npln-tenant-id`, `authorization` and `uid`. Malformed
   metadata is rejected the way Nintendo's server does: status 12 on production,
   14 on development.
4. **Auth**: exchange a BAAS `id_token` for an NPLN session, verifying it against
   `/1.0.0/certificates`. **This is the Phase 1 → Phase 2 seam** and the first
   thing that proves the two halves connect.
5. **`MaintenanceScheduleService`** answering "no maintenance".

That is the slice: a gRPC server a Go test client can drive end to end, that
authenticates a real Phase 1 token and answers one real RPC. Everything after it
is additive.

**Then**, in rough boot order: the `nplnrest` front (which the game actually hits
first, but which is harder to verify without a console than the gRPC half),
`Schedule`, `gamesync`, matchmaking, and `FestService` + `internal/bcat`.

### Identity, unchanged

`internal/npln`'s session lookup is fail-closed, for the same reason
`internal/baas` is: a session that cannot be resolved to a user is an error, never
a default. The bug this repository is shaped around was a fallback in exactly
this position, and a game server that resolves the wrong player is the same bug
wearing a different hat.

---

## Splatfest mechanics

Enough is known to design this properly; the parts that are not are flagged.

**The pack encryption** is `nisasyst`: AES-128-CBC, where the **IV is derived
from the resource name** (CRC32 of the name, then SeadRandom) and the **key comes
from `GetFestDecryptionKey`** *(observed in reference)*. Both inputs are the
server's to choose, which is what makes genuinely custom fests possible rather
than merely renaming Nintendo's.

Three things must agree, or the game says `BcatInvalid`:

1. the pack, encrypted with our key under our resource name;
2. the announced `festid` / `festressource` / `festrevision`;
3. `GetFestDecryptionKey`'s answer, naming the same fest and carrying the same key.

**Case matters, in opposite directions** *(observed in reference)*: the fest id is
uppercase (`NXTD-00042`), the resource name lowercase — because the IV is derived
from the resource name, so one wrong capital and the pack will not open. This is
the kind of detail that costs an evening; `internal/bcat` should enforce both
rather than trusting a caller.

**Timestamps must land on two-hour boundaries.** Splatoon 3 buckets schedules
into 7200-second slots. A fest whose timestamps are not ≡ 0 (mod 7200) falls into
no slot; the game stops treating it as open and demands a verdict instead
*(observed in reference — the reference records eleven consoles receiving such a
fest and none being able to play)*. The phase offsets are all multiples of 7200,
so aligning the start aligns the whole table:

| Phase | Offset from start |
| --- | --- |
| announce | −662400 s (7 d 16 h) |
| start | 0 |
| midpoint | +86400 s (24 h) |
| end | +172800 s (48 h) |
| close | +180000 s (50 h) |

Four regions — `JP`, `US`, `EU`, `AP` — each with a publication window opening
25200 s before the announcement and running 3628800 s.

The schedule response carries **no artwork at all**: resource names, region
codes, a target, the pack id and its 40-hex fingerprint, five timestamps, the
publication window, and two tricolour stage numbers. That is editorial choice
only, which is why SwitchNet can generate one wholesale without reproducing
anything of Nintendo's.

---

## What is genuinely capture-gated

Marked `TODO(capture-needed):` in the source and listed in
[`CAPTURES/README.md`](CAPTURES/README.md) when the code lands, per the existing
convention. The reference does not settle these, because its measured bytes are
not in it.

| Unknown | What a capture settles | Where it would change |
| --- | --- | --- |
| vermillion request/response bodies | the JSON shapes for device init and account config | `internal/nplnrest`, `vermillion` |
| penne login-ticket shape, and the field the frontline FQDN comes back under | what the game needs before it will open the frontline | `internal/nplnrest`, `penne` |
| the frontline binary framing | what those frames mean; the reference logs them as opaque hex | `internal/nplnrest`, `frontline` |
| the BCAT pack container | the archive layout and `summary.yml`, around known crypto | `internal/bcat/pack.go` |
| tricolour stage numbering | which stages are valid, and what an invalid one does | `internal/toyohr/fest.go` |
| whether a Phase 1 `id_token` satisfies S3 | the auth seam, end to end | `internal/npln/auth.go` |

Per CLAUDE.md: implement the most defensible reading, mark it, name the single
function a capture would change. Do not invent a plausible-looking Nintendo
internal.

**The first three of those are now one operator action rather than six.** Set
`npln.rest_record_dir`, put a console through one boot, and `internal/nplnrest`
writes every request and response to disk — hexdumped, in order, credentials
redacted. Building that recorder was the useful half of implementing a front end
whose shapes nobody knows.

---

## Open questions for the operator

1. **Certificate pinning — there is a known route, and it needs a decision.**
   Splatoon 3 pins its own certificates independently of the console's CA store,
   so Phase 1's LayeredFS work does not cover it. `kinnay/NPLN-Protocols` ships
   `generate_patch.py`, which builds an IPS patch disabling TLS certificate
   verification in the game's main NSO — written for exactly this purpose
   ("if you want to capture traffic or write your own NPLN servers"). The
   reference reaches hardware the same way, with game patches.

   The decision is one this repository has already taken a position on in a
   neighbouring case, and the position does not transfer cleanly.
   [`ROADMAP.md`](ROADMAP.md#phase-1--accounts-and-the-stock-friends-list)
   rejects the Prelude-style route partly because it "turns off certificate
   verification system-wide". An NSO patch is meaningfully narrower — one
   game's binary, not the system TLS stack — but it is still *disabling*
   verification rather than *adding* trust, which is the property Phase 1
   deliberately preserved. It is also firmware-independent but game-version
   gated, so it silently stops applying after a title update.

   Recommend accepting it for Splatoon 3 specifically, with the narrowness and
   the version-gating written down where an operator will find them. But it is
   the operator's call, and nothing in the first slice depends on it.
2. **STUN/TURN.** ROADMAP §2.3 is right that Splatoon 3 uses ICE, not the Pia
   NAT-check pair, so `natcheck` is not involved and coturn becomes a deployment
   dependency. Is standing that up in scope for this phase?
3. **Where do custom fest assets come from?** The mechanism is understood; the
   art is not something this repository can generate. Operator-supplied files, or
   a small authoring tool?
4. **Dashboard authentication.** Nothing in Phase 1 authenticates a human — every
   credential so far belongs to a console. The dashboard needs its own answer.
5. **`redis.url` must be set before the dashboard runs as its own process.** The
   in-process `presencebus` delivers nothing across a process boundary, and it
   does so silently. Worth a `switchnetctl check` validation, per the config rule.
