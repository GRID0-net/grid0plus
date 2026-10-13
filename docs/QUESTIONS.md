# Open questions for the operator

Questions an agent could not answer for itself, recorded here instead of
blocking on them.

**This file exists so that work never stops.** An agent working autonomously
cannot ask anything, so the rule is:

1. Write the question here, with enough context that it can be answered cold.
2. State the **assumption you proceeded on** and where in the code it lives.
3. Keep going.

An assumption written down and implemented is worth far more than a question
asked and waited on. The operator answers these in batches; when one is
answered, act on it, then delete the entry — `git log` is the record.

**Format.** One `##` per question. Always include *Proceeding on*, and name the
single file or function that changes if the answer differs — that is what makes
an answer cheap to act on later.

---

## Is there a STUN/TURN (coturn) deployment, and at what addresses?

Splatoon 3 uses ICE rather than the Pia NAT-check pair, so `AllocateIceServerSet`
has to hand the console real STUN/TURN addresses — and they must be reachable
**from the console**, not merely from the server. Wrong addresses produce "the
lobby fills but the match never starts", which is indistinguishable from a
matchmaking bug when read from the server side.

**Proceeding on:** `npln.ice_servers` in config, empty by default, served
verbatim by `AllocateIceServerSet`, with a `switchnetctl check` warning when
unset. Nothing is discovered: a server behind NAT cannot know its own reachable
address, and a guess there produces exactly the failure above. **This is
built** — an empty list means no ICE servers are offered, and matchmaking
works for peers on the same LAN without any of it.

So the question is now only *what to put in the file*, not what to write. If
coturn exists, its addresses and a TURN username/password go in
`config.yaml`; if it does not, this stays empty and consoles on one LAN are
unaffected.

**Changes if answered:** `config.yaml` only. One further thing would need code
if coturn is deployed with the time-limited HMAC credential scheme rather than a
static user: `AllocateIceServerSet` in `internal/npln/matchmaking.go` would have
to derive the username, which it currently does not.

---

## Does the game key Splatfest assets on the announced fest id, or only on the pack it is handed?

A real custom pack (`juea-lovefes2`, announced as `JUEA-00210`) contains files
still named for the fest it was derived from:

```
Mals/Msg_EUde.Fest_JUEA-DUMPLING_Bh.sarc.zs
RSDB/Tag.Fest_JUEA-DUMPLING.dev.rstbl.byml.zs
```

If the game resolves assets by the announced id, a from-scratch fest needs every
internal path renamed **and** the RSDB/rstbl tables rebuilt. If it just reads
the pack it was given, the names are inert and this is a non-issue.

**The experiment:** build a pack whose internal names match its own fest id, put
it on a console, see whether the fest loads.

**Proceeding on:** `bcat.substitute_fest_id` is **on** by default, and an asset
path containing `{FEST}` gets the announced fest id substituted in. An operator
who writes literal names gets them left alone, so both readings are reachable
without a code change.

**Independent corroboration, 2026-09-05:** `NextendoNetwork/splatoon-3` — a
from-scratch NPLN reimplementation the operator supplied and that repository
says was tested against real hardware (PolyForm Shield-licensed, so only
this design fact is taken here, never its code) — resolves the underlying
question the same way in production. It cannot synthesise Nintendo's own
encrypted packs either, so instead of building one it announces whatever
fest identity the console's *existing* BCAT cache already holds, rewriting
its served schedule's fest id/resource/revision to match a cache the
operator supplies out of band (`fest_align.go`'s `festalias`). That only
works if the console resolves fest resources by the *announced* id against
the cache's own top-level metadata, not by names baked into the pack's
internal files — the same "names are inert" assumption `substitute_fest_id`
defaults to. GRID0+ does not need the same rewrite mechanism
(`SelectFestSchedule` builds its response from a database row rather than
replaying a captured one, so an operator can already set any
`resource`/`revision` directly via `switchnetctl fest build`/the dashboard's
create-fest form), but the underlying assumption this question is about is
the same one their production server depends on working.

**Changes if answered:** `internal/bcat.Naming.Apply`, and nothing else — the
naming decision was deliberately confined to that one function so that acting on
an answer is a one-line change rather than a rewrite.

---

## ~~Do sigpatches cover the `bcat` sysmodule?~~ — resolved: no, so GRID0+ ships its own patch

`sys-patch` covers `fs`, `es`, `ldr` and `nifm`, not `bcat`. Reading
`bcat.nso` (22.5.0) showed the container signature is verified and enforced,
so `switch/atmosphere/exefs_patches/bcat_signature_bypass/` skips that check,
and `internal/bcat` serves the list and data responses. A real console
downloaded all five packs of a custom Splatfest natively on 2026-10-09. See
`TODO.md` §2.4 and `docs/BCAT_NATIVE.md`. A firmware update needs the patch
re-derived; `switch/bcat-installer/` stays as the fallback until then.

## Is there a decryption key for the LoveFest pack?

`internal/bcat`'s IV derivation is verified byte-for-byte against the reference
implementation, but the full decrypt path has never been run against real
ciphertext with a known-good plaintext. The encrypted and decrypted LoveFest
packs both exist; the key does not.

**Proceeding on:** the IV match plus a round-trip test is sufficient evidence.
This would close it completely rather than fix anything.

**Changes if answered:** adds a test to `internal/bcat`; changes no behaviour.

---

## Does the console treat an all-`is_valid: false` FestResult as "not concluded"?

`GetFestResult` now answers rather than returning `Unimplemented`, but every
sub-result (`Yobisai`, `HonsaiMidterm`, `HonsaiFinal`, `Overall`) reports
`IsValid: false` -- GRID0+ has no real battle-outcome data to compute a
ratio or point total from (`npln_fest_entries` records team *choice*, not who
won, and `GameRecord` is unimplemented), and deriving one from entry counts
alone would be actively misleading rather than merely incomplete.

Two readings are both plausible for what the console does with an
all-invalid result: treats the fest as still running (the same as before
`GetFestResult` existed, since it previously errored), or shows an empty/tied
outcome. Nothing here can tell which without seeing it happen.

**Proceeding on:** `IsValid: false` is what the proto's own field exists to
say, so it is the defensible reading rather than inventing a plausible ratio.

**Changes if answered:** `internal/toyohr/entries.go`'s `GetFestResult`.
If the console needs a genuinely valid result, the entry-count-derived ratio
this pass rejected becomes the next thing to reconsider -- explicitly, in a
commit that says why it is no longer considered misleading enough to avoid.

---

## What do `CoopSchedule`'s `Name`/`ScheduleSetId`/`ShiftId`/`RewardType` fields actually carry?

`internal/toyohr/coop_schedule.go`'s `coopScheduleProto` builds a real
`nn.npln.toyohr.v1.CoopSchedule` message (see `internal/toyohr/README.md`'s
"honest emptiness for Vs, a real rotation for Coop" section for why the
message *shape* itself is well-confirmed) but four of its fields have no
confirmed content: `Name`, `ScheduleSetId` and `ShiftId` are opaque
identifier strings with no known real format, and `RewardType`
(`CoopSchedule_Normal`/`_BigRun`'s own field) is presumably an enum-like
string this project has not seen a real value for. `Name`/`ScheduleSetId`/
`ShiftId` apply to `CoopSchedule_TeamContest` too, once that kind got an
authoring path; `RewardType` does not, since `CoopSchedule_TeamContest` has
no such field to leave unset in the first place.

**Proceeding on:** `Name`/`ScheduleSetId`/`ShiftId` all reuse the derived
shift id (`toyohr.CoopShift.ID()`) under the `tenants/<t>/coopSchedules/...`
prefix `GetFestDecryptionKey` already confirms for a different resource —
the same "lenient, consistency-over-invention" reasoning
`festEntryIDsFromResourceName` uses below. `RewardType` is left `""`
(unset) rather than guessed, on the two kinds that carry it. Unlike
`Fest.Resource`, nothing computationally depends on any of these: no
decryption IV is derived from them, so a wrong guess costs a cosmetic
mismatch, not a broken RPC or a pack that fails to open.

**Changes if answered:** `internal/toyohr/coop_schedule.go`'s `coopScheduleProto`.

---

## ~~What does `CreateFestEntry`'s response actually name a new entry?~~ — resolved

NextendoNetwork/splatoon-3's notes on a real-console capture (facts only,
PolyForm Shield, never its code) record the shapes: the request is
`parent = tenants/current/fests/<FEST-ID>`, the response `name` is
`tenants/<tenant>/fests/<FEST-ID>/entries/<user id>`, and `GetFestEntry`
asks for `tenants/current/fests/<FEST-ID>/entries/current`. That is exactly
what `festEntryProto` and `festEntryIDsFromResourceName` in
`internal/toyohr/entries.go` already produce and parse.

## ~~Does Splatoon 3's boot sequence need anything from vermillion at all?~~ — resolved: yes

NextendoNetwork's capture notes (facts only) show a real console calling
`POST /v1/devices/initialize` (answered 204), `GET /v1/devices/vermillion-device-id`
(`{"vermillionDeviceId": "<base64, 16 bytes>"}`), the penne-id registration and
the accounts/config reads before it ever reaches penne, and looping on the
device bootstrap when any of them fails. So vermillion is an NPLN prerequisite,
`hosts.go`'s classification is right, and `internal/nplnrest` answers it. A
real console on GRID0+ has been through this stage since 2026-09-25. The two
endpoints still answering an empty envelope (`acds/search`,
`nearness-checks`) are tracked in `TODO.md` §1.3.

## ~~How does a console obtain a Penne id and password?~~ — resolved: it makes them up

The same capture notes show the client fabricates the id and the password
itself; the password never appears in any server response. Registration only
has to *succeed*: `POST beach.hac.lp1.eshop.nintendo.net/v1/devices/<device
id>/penne_id/register` and `PUT dragons.hac.lp1.dragons.nintendo.net/v2/penne_id`,
then `POST /v1/login_tickets` with the id and password. GRID0+ answers all of
these (`internal/stubs` for beach and dragons, `internal/nplnrest` for the
rest), so a fresh console gets through with no provisioning step. The `god`
host was never the one.

## What does a frontline connection actually carry?

`internal/nplnrest/nplnrest.go`'s `frontline` accepts the `fro-N`
connection and holds it open — confirmed safe, since
`internal/nintendo/hosts.go` already establishes from a real error-code
capture that closing it reads as a failure to the console. What happens
**inside** the open connection (a single long HTTP response body, a
WebSocket upgrade, framed messages of some kind) is not documented
anywhere this pass could find, on the wiki or otherwise.

**Proceeding on:** answer nothing further down the connection — silence
risks a keep-alive timeout the console might not expect, but inventing a
frame format risks the console rejecting it outright the moment it parses
the first byte, which seemed the worse failure to court blind.

**Changes if answered:** `internal/nplnrest/nplnrest.go`'s `frontline`,
entirely.

---

## ~~Does Splatoon 3 really reach gamesync through the tenant channel, or on its own port?~~ — resolved: its own port

An earlier pass classified `gamesync.npln.nintendo.net` as `ServiceNPLN` --
the same service as the tenant gRPC host -- and registered
`internal/gamesync` on `internal/npln`'s own `*grpc.Server` alongside Auth,
Matchmaker, GameSessionService and `internal/toyohr`'s services. That reading
did not survive contact with the rest of the evidence: `gamesync.npln.
nintendo.net` not ending in `srv.nintendo.net`, unlike every other NPLN host,
turned out to be exactly the deliberate signal it looked like. `internal/
nintendo/hosts.go` now classifies it `ServiceGamesync`, a distinct service on
its own port (7575, `TLSPort` in the host entry) rather than through the
tenant host's nginx block -- see that package's own comment for the
consequence of getting this wrong (the console de-duplicates a probe sent to
the same address twice, the same class of bug the nncs1/nncs2 note in
CLAUDE.md warns about for NAT-check).

`internal/gamesync` is now a complete, self-contained gRPC service (its own
`Server`, `Options`, `Serve`/`Stop`, and its own matchmaking-id-token
verification via a `TokenVerifier` callback into `internal/npln`) rather than
a service registered on npln's gateway. It still runs inside `cmd/npln` --
splitting it into its own process would mean inventing a protocol to share
the signing key -- but as a second listener on `npln.gamesync_listen`, not a
second service on the first one. See `internal/gamesync/README.md`.

**Changes if answered:** if a capture shows the console dialing a distinct
address/port for Gamesync, `internal/gamesync.Service` moves to its own
listener (`config.NPLN` would need a new listen address back), and
`gamesync.npln.nintendo.net`'s entry in `internal/nintendo/hosts.go` would
need a `Service` of its own rather than sharing `ServiceNPLN`.

---

## How should the dashboard authenticate a person, long term?

Today it is a shared token (`dashboard.token`); unset, there is none, so anyone
who can reach the port can schedule and cancel Splatfests.

**Answered (4301256, bot cb06257):** the bot hands a 15-minute login link,
signed with the dashboard token, to a Discord member holding the configured
admin role (`internal/dashboard/loginlink.go`). The token stays the one
credential underneath; per-person sessions wait for a reason to tell admins
apart.

---

## Does a first-time Nintendo Account link actually request any of the eight unserved `accounts.nintendo.com` paths?

Decompiling the console's own `account` sysmodule (fw 22.5.0) shows it can
construct eight `accounts.nintendo.com` URLs that GRID0+ does not route:
`/profile`, `connect/1.0.0/api/term_agreement_token`,
`connect/1.0.0/term_confirm`, `connect/1.0.0/api/transfer_na_tokens` (plus its
`/%lld` and `_inherit` forms), and `analytics/device/opt_in` with its `app/`
variant. All are first-link surface, and the terms pair matches the licence
scene in the add-user wizard. See
[`CAPTURES/account-sysmodule-decompile.md`](CAPTURES/account-sysmodule-decompile.md).

This is the open candidate for the post-federation "account couldn't be linked"
dialog: if the console walks terms → analytics → profile after federation,
every one of those requests currently gets a 404. It needs no MITM to
answer — `docker compose logs nginx` from the failed attempt already holds every
request line, and `CAPTURES/nintendo-account-link.md` documents the working
tail to compare against. **This can only be answered on the console**, which is
why it is here rather than in `TODO.md`.

**Proceeding on:** nothing is implemented for these paths, and no response
shape is guessed for them. A URL appearing in the binary proves it is
constructible, never that it is called.

**Changes if answered:** `internal/baas/router.go` plus one handler next to
`naAuthorizePage`/`naToken`. If the log shows a request, the response shape
comes from the parser that consumes it — `0x1c1eb0` reads
`$.analyticsPermissions.*` with `$.birthday`/`$.country`, and `0x1c44a0` reads
the flattened `$.analyticsFor*` with `$.isNnLinked` — not from invention. If the
log shows none of them, they stay unimplemented and the dialog's cause is
somewhere else.
