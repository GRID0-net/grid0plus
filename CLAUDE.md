# CLAUDE.md

Notes for an agent working on this repository. Read this before changing code.

`README.md` is for a person who wants to run GRID0+.
`docs/ARCHITECTURE.md` is for a person who wants to understand it.
This file is for you: the things that are easy to get wrong, and the reasons
behind decisions that look arbitrary.

---

## What this project is

A self-hosted replacement for the Nintendo servers a Switch talks to, for a
private group of consoles the operator owns.

Five phases, tracked in [`docs/ROADMAP.md`](docs/ROADMAP.md):

| Phase | Scope | State |
| --- | --- | --- |
| 1 | accounts + the stock friends list | **running on a real console**: sign-in, Nintendo Account linking and the friend list work; the received-requests view still shows 2124-0104 (client-side, parked) |
| 2 | NPLN game server for Splatoon 3, and a dashboard | **running on a real console**: hall, Turf War (one network and across networks), private battles, Salmon Run (incl. Big Run and "Again!"), the Splatfest hall with native BCAT, and two-player fest battles have all worked on hardware; the newest fest matchmaking flow is not yet tried — see [`docs/ROADMAP.md`](docs/ROADMAP.md#phase-2--splatoon-3-npln--dashboard) |
| S2 | NEX game server for Splatoon 2, and a dashboard tab | code complete; exercised end to end with Kinnay's NintendoClients, **not yet tried on a console** — see [`internal/splatoon2/README.md`](internal/splatoon2/README.md) |
| 3 | eShop, served from local NSP dumps | **out of this repository** — planned as a separate, optional module. Not started. See [`docs/ROADMAP.md`](docs/ROADMAP.md#phase-3--the-eshop-a-separate-module) |
| 4 | per-game cloud save backup (OLSC) | code complete on a defensible-but-unconfirmed shape, **not yet tried on a console, storage backend local disk**. See [`docs/ROADMAP.md`](docs/ROADMAP.md#phase-4--cloud-save-backup-olsc) and `internal/scsi/README.md` |
| 5 | account refactor + the Discord bot | **built and deployed**: the bot ([`GRID0-net/grid0plus-discord`](https://github.com/GRID0-net/grid0plus-discord)) is the only way to create a person's account, through `internal/baas/botapi.go`; a console's pre-link placeholder is `provisional` and deleted when it links. Not yet tested end to end on a console. See [`docs/ROADMAP.md`](docs/ROADMAP.md#phase-5--account-system-refactor--a-discord-bot) |

**Phase 3 does not belong in this codebase.** If you're asked to start eShop
work, the first question is where that code should live — not `internal/`
here. This repo's own `nim`/dauth/BAAS work stays in scope for anything the
eShop *depends on* (identity, device auth); the storefront, wallet, rights
and content-delivery layers do not.

**Phase 4 does belong here**, unlike phase 3 — it needs no game dumps and
carries none of phase 3's copyrighted-content exposure, just real storage
cost that grows with usage. The storage backend decision is made: local disk
on the box already running GRID0+, under `scsi.storage_dir` and
`scsi.per_user_quota_bytes` in `config.yaml` — see `internal/scsi/storage.go`.
Most of `internal/scsi`'s request/response shapes are confirmed against
Kinnay's public wiki page for OLSC; the two `generate_key_*package`
endpoints are this project's own defensible placeholder, not a confirmed
shape, and are the most likely reason a real console would still refuse a
restore. Nothing has settled them yet — see `internal/scsi/README.md`.

**Phase 1 works on a real console, but not every friends shape is settled.**
The friend list renders, which confirms that response; the rest of
`internal/baas/friends.go` is still provisional — see
[the guesses](#the-guesses-and-how-to-treat-them) below.

## Ground rules

1. **Never commit a Nintendo key, certificate, ticket or dumped file.** Not to
   test fixtures, not as a "temporary" constant, not base64-encoded in a doc. If
   a task seems to need one, it is the wrong approach — say so instead of finding
   a way.

2. **Never verify a Nintendo-issued credential and never forge one.** Device
   certificates, tickets and gamecard challenges are accepted without being
   looked at. That is a deliberate design decision with a table of consequences
   in
   [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#what-is-deliberately-not-verified);
   do not "fix" it.

3. **Identity is fail-closed. Do not add a fallback.** If a lookup cannot resolve
   who is calling, the answer is an error. See
   [below](#the-two-bugs-this-codebase-is-shaped-around) for what happens when
   somebody adds one.

4. **Run it, do not just test it.** Three of the bugs fixed so far were invisible
   to a green test suite and obvious within thirty seconds of starting the
   processes. `make up`, then `make run` in one terminal and `make seed` in
   another, is cheap.

5. **One logical change per commit**, conventional messages (`feat:`, `fix:`,
   `docs:`, `test:`, `refactor:`). The body should say *why*, not restate the
   diff. Look at `git log` for the register.

6. **If a Nintendo request or response shape is genuinely undocumented, do not
   invent a plausible-looking Nintendo internal.** Implement the most defensible
   reading, mark it `TODO(capture-needed):` with what a capture would settle and
   which single function to change, and add it to
   [`docs/CAPTURES/README.md`](docs/CAPTURES/README.md).

## Getting oriented

```sh
make help                 # every target
make check                # gofmt, go vet, go test — what CI runs
make up                   # Postgres, Redis and nginx (Docker)
make run                  # the nine Go services, natively, foreground
make seed                 # in another terminal: drive it over HTTP
```

The tests need no database: the handler tests use an in-memory store and finish
in about twenty seconds. To also run the integration tests:

```sh
make test TEST_DATABASE_URL="postgres://…" TEST_REDIS_URL="redis://…"
```

Read in this order: `docs/ARCHITECTURE.md`, then `internal/baas/README.md`, then
`internal/baas/router.go`.

Iterating on one service: `bin/switchnetctl run baas` (or `dauth`, `npln`, ...)
builds and runs just that one natively against the still-Dockerized Postgres/
Redis — a `go build`, not a full rebuild — instead of running all nine. Every
service reads the same `certs/keys` directory `config.yaml` already points at;
there is no separate dev-only key directory to keep in sync with it.

## The two bugs this codebase is shaped around

Both were reported on real hardware, against the production server GRID0+
replaces. They are written up in
[`n-popescu/splatoon-3`'s `docs/FRIENDS.md`](https://github.com/n-popescu/splatoon-3/blob/main/docs/FRIENDS.md).
Most of the non-obvious code here exists because of one of them.

### Every console acted as one person

That server resolved a console by its device account id and, when the lookup
missed, **fell back to a process-wide variable holding the last account that had
authenticated**. A console caches its device account forever, so an unrecognised
credential is a completely normal situation — and that is exactly when the
fallback fired. Result: every console sent friend requests as one person and
displayed his friend code, and regenerating the code changed nothing, because the
code was never the identity.

What holds the line here:

- `internal/baas/router.go`, `authenticate` — re-reads the device account on
  every request and refuses if it no longer belongs to the token's subject;
- `internal/baas/accounts.go`, `completeLogin` — one lookup, constant-time
  password compare, `invalid_device_account` on any miss, no other outcome;
- `requireSelf` on every user-scoped endpoint;
- the self-request guard in `createFriendRequest`.

The tests: `TestLoginRefusesAnUnknownDeviceAccount`,
`TestATokenWhoseSubjectDoesNotOwnItsDeviceAccountIsRefused`,
`TestOneUserCannotReadOrChangeAnother`,
`TestAnExistingTokenStopsWorkingWhenItsDeviceAccountIsDeleted`. **If you find
yourself weakening one of these, you are reintroducing the bug.**

### Friends were never online

Two causes. Nothing reported the presence of a console that was simply on, and a
reported state never expired.

`internal/baas/presence.go`, `resolvePresence`:

```
fresh report?           → use it exactly, including OFFLINE
otherwise seen recently? → ONLINE, app fields cleared
otherwise                → OFFLINE
```

Three details, each of which was a bug at some point:

- **A console reports nothing in the Home Menu** but keeps polling. Any
  authenticated request bumps `last_seen_at`. Without that branch every friend is
  permanently offline.
- **"Never reported" is not "reported OFFLINE".** `presence.reported_at` is
  NULL until the console reports. Defaulting it to `now()` makes a new account
  look like it just went offline and suppresses the heartbeat.
- **Clear the app fields whenever the effective state is not `PLAYING`.** A
  console that closes a game reports `ONLINE` without clearing them, so the
  friend list keeps showing "playing X".

## Things that will waste your time

**All services must share one `keys.dir`.** `aauth` verifies `dauth`'s device
tokens; `baas` verifies both. Different directories do not error — every request
is rejected with "device token expired". Key generation writes a temporary file
and `os.Link`s it into place so that five services starting at once converge on
one key; do not replace it with `os.WriteFile`, and do not "simplify" it back to
an `O_EXCL` create onto the final path.

That last one is not hypothetical: it *was* an `O_EXCL` create, and it published
the filename before the contents. The loser of the race saw `EEXIST`, read the
file, and found it empty — "no PEM block" from a directory that visibly holds a
key. RSA-2048 generation is slow enough to hide it; ES256 generation is fast
enough that it showed up under `-race` within a handful of runs.

**`nncs1` and `nncs2` must be different IP addresses.** Pia probes them as two
servers and one NAT-check message type must be answered from a different address.
Same IP → the console de-duplicates the probe → NAT detection never completes →
matchmaking finds matches that will not start. The config validator refuses to
let them be equal.

**Do not redirect the `d4c` system-update hosts to a GRID0+ endpoint.** `nim`
writes a persistent flag when the update check fails, and it survives switching
back to Nintendo's servers — the general-purpose reason to leave them
`ServicePassthrough`. This deployment's own emuMMC is permanently and
exclusively GRID0+'s, so that flag can never survive into a "switch back"
that will never happen, and passthrough would only mean real Nintendo traffic
leaving the console for no benefit: `internal/nintendo/hosts.go` marks `sun`/
`atumn`/`aqua` `ServiceBlackhole` (null-routed to `0.0.0.0`) instead, with
`TestSystemUpdateHostsAreNullRouted` guarding that choice and its reasoning. If
you ever reuse this host list for a console that *does* move back and forth
between GRID0+ and real Nintendo, put these back to `ServicePassthrough`
first.

**Never let Splatoon 3's fest schedule go empty, and keep a fest's BCAT pack
served while it is announced.** The game has no "no fest" answer: an empty
`SelectFestSchedule` leaves its fest with an all-zero timetable, which it
treats as Invalid. Every big screen in the lobby sits on a fest layer, so
Invalid made them blank for every player (2026-10-17 to 2026-10-19). A fest
whose results are out is "no fest" to the game (state None, the FestNone
layer), so `lastClosedFest` keeps announcing the most recent closed fest
until a newer one exists. So when an operator disables or deletes fests,
there must still be a closed one left, with its seed in
`data/bcat-native/0100c2500fc20000`; without the pack the console reports
BcatInvalid. Deleting a fest also strands saves on it: `dropDeletedFest`
cleans those up. Do not "tidy" the fallback away, and do not empty
`bcat-native`.

**Redirection and certificate trust are separate problems.** Redirection is DNS
and happens off the console, in a resolver the operator already runs — the
generated dnsmasq/unbound configuration, never GRID0+ itself. Certificate
trust has to happen on the console, because the cert for
`dauth-lp1.ndas.srv.nintendo.net` is signed by a private Nintendo CA. Do not
conflate them: a report of "nothing in the log at all" is almost always trust,
because the connection never becomes a request, while DNS resolving correctly
(check the resolver's own logs) but nothing arriving at dauth is trust failing
after DNS worked.

**Do not forward AAAA for a redirected name.** The generated dnsmasq/unbound
configuration answers NODATA. If it forwarded, a console on a network with
IPv6 would resolve Nintendo's real address and bypass GRID0+,
intermittently — close to undiagnosable from the console side. Same reason
the generated unbound config uses `local-zone ... static` rather than
`transparent`.

**Hostnames live in exactly one place.** `internal/nintendo/hosts.go` generates
the DNS answers, the dnsmasq and unbound configuration, the nginx map, the
certificate SANs and the hosts file. Adding a host anywhere else produces a
failure with no useful error.

**`sudo mitmdump` may sign certificates from `/root/.mitmproxy/`, not
`~/.mitmproxy/`.** Both directories can hold an independently-generated CA
under the exact same default name, `CN=mitmproxy, O=mitmproxy`, so nothing
about the Subject line reveals the mismatch. Editing/replacing the wrong one
looks correct at every step and silently has no effect — the browser applet's
TLS handshake will keep dying right after the ClientHello no matter what you
change. Before touching a mitmproxy CA for a capture, confirm which directory
the running process is actually using — see
[`docs/PACKET_CAPTURE.md`](docs/PACKET_CAPTURE.md#sudo-mitmdump-may-not-be-the-mitmproxy-you-think-it-is).

## The guesses, and how to treat them

The BAAS `2.0.0` friends endpoints are **not documented anywhere** and no public
implementation exists to compare against. Several shapes are reasoned rather
than known; the friend list itself is now confirmed by the applet rendering it
on hardware. The live list:

```sh
grep -rn 'TODO(capture-needed)' internal/
```

Priority order and what each would settle are in
[`docs/CAPTURES/README.md`](docs/CAPTURES/README.md). "Capture" there never
means decrypting Nintendo's traffic from the console (see
[where to ask the operator](#where-to-ask-the-operator-rather-than-guess));
a shape is settled by RE of the module that reads it, GDB on the emuMMC, or a
capture of the console talking to GRID0+.

When one is settled:

1. write it up under `docs/CAPTURES/` using the template, redacted;
2. reconcile the implementation;
3. **delete the `TODO(capture-needed)` comment in the same commit**, so the source
   and the notes cannot disagree.

Do not quietly change a guessed shape without a capture. It is currently a
documented reading with a rationale; replacing it with a different guess loses
the rationale and gains nothing.

## Conventions

**Comments explain why, never what.** `// increment i` is noise. The comments
worth writing are the ones that stop the next person undoing a decision —
"answered from the secondary port because there is no second address; see the
package comment for what it costs".

**Every `internal/` package has a `README.md`** naming the Nintendo endpoints it
reimplements and linking the Kinnay wiki page it was built from. Keep it current.

**Tests assert on the wire, not on Go structs.** `internal/baas/tokens_test.go`
decodes tokens by hand, because a claim that is present in the struct but
mis-tagged would round-trip through the verifier and still not match a capture.
Assert on absences too: an application token has no `aud`, an anonymous token
has no `bs:did`.

**Assert an absence only when a capture actually shows it absent.** This section
used to also claim "a BAAS token has no `typ` header", and a test enforced it.
It was wrong: every one of the twelve JWTs in the decrypted capture of a working
server carries `typ: JWT`, BAAS tokens included. The claim most likely came from
confusing the *header* `typ` (the standard `JWT`) with the *payload* `typ`
(BAAS's own `token`/`id_token`), which really is absent from some tokens. A
wrong asserted absence is worse than no assertion: it is a test actively holding
a bug in place, and it survived far longer than it should have because the rule
was written here as though it were established.

**A test name should say what breaks.** `TestAnIdleConsoleReadsAsOnlineNotOffline`
over `TestPresence3`.

**Two store implementations, one contract.** `internal/db/store_test.go` runs
against both PostgreSQL and the in-memory store. If you add a `Store` method, add
it to the contract — that test has already caught a real bug where an empty id
filter matched every user under one implementation and none under the other.

**Errors the console reads.** It switches on `errorCode`, not the HTTP status, so
the code matters more than the status. Only the errors GRID0+ can actually
produce are defined in `internal/baas/errors.go`; do not add ones for checks that
are not performed.

## Configuration

Everything is in `config/config.go`, with `SWITCHNET_*` environment overrides and
a `config.example.yaml` that is itself the documentation. Two rules:

- **Unknown YAML keys are rejected.** A silently ignored key is a setting the
  operator thinks they changed.
- **Validation rejects what cannot work and warns about what causes trouble.**
  `switchnetctl check` is the entry point. If you add a setting whose wrong value
  produces a confusing symptom, add the check.

## Before you finish

```sh
make check          # gofmt, vet, test
make test-race      # the servers are concurrent by nature
make build
make up             # Postgres, Redis and nginx
make run &          # the nine Go services
make seed           # drive it over HTTP
```

Then check that the generated artefacts still generate:

```sh
SWITCHNET_PUBLIC_IP=192.168.1.50 SWITCHNET_NATCHECK_SECONDARY_IP=192.168.1.51 \
  go run ./cmd/switchnetctl hosts | head
```

And that nothing that should not be tracked is:

```sh
git ls-files | grep -E '\.(pem|key|crt)$|^certs/|config/config\.yaml'   # must be empty
```

## Where to ask the operator rather than guess

- anything needing a packet capture. Against Nintendo's real servers that can
  only be a tcpdump (hosts, timing, sizes): the operator does not run
  Atmosphère there, so nothing is decrypted. For a request/response shape,
  prefer static RE of the game or sysmodule, live GDB on GRID0+'s own
  emuMMC, or a decrypted capture against GRID0+ itself;
- whether the CertStore LayeredFS override actually reaches the `ssl` sysmodule —
  there is a five-minute test in `switch/README.md` and only the operator can run
  it. `fs.mitm` does not MITM arbitrary system modules, so this is genuinely
  open;
- anything needing a game dump, a title key, or `prod.keys`;
- whether to prioritise fidelity to Nintendo's shapes over a simpler design, when
  the two conflict;
- anything that would send data off the operator's own network.
