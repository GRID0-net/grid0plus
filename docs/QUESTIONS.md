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
defaults to. SwitchNet does not need the same rewrite mechanism
(`SelectFestSchedule` builds its response from a database row rather than
replaying a captured one, so an operator can already set any
`resource`/`revision` directly via `switchnetctl fest build`/the dashboard's
create-fest form), but the underlying assumption this question is about is
the same one their production server depends on working.

**Changes if answered:** `internal/bcat.Naming.Apply`, and nothing else — the
naming decision was deliberately confined to that one function so that acting on
an answer is a one-line change rather than a rewrite.

---

## Do sigpatches cover the `bcat` sysmodule?

See TODO §2.4 for the full experiment. If they do, serving BCAT natively may be
possible and `switch/bcat-installer/` becomes unnecessary. `sys-patch` covers
`fs`, `es`, `ldr` and `nifm`; `bcat` is not on that list.

**The server for stage 1 is written and waiting.** `internal/bcat.Probe` logs
every request the `bcat-*` hosts receive and writes a hexdumped transcript when
`stubs.bcat_record_dir` is set. Running the experiment is three lines in
`internal/nintendo/hosts.go` plus a config value — see TODO §2.4.

**Proceeding on:** it is not covered, so SwitchNet builds the delivery cache and
does not serve BCAT. Note that this is only *half* the blocker — the `list`
response body is undocumented regardless, and stage 1's transcript is what would
start to settle it.

**The experiment is now five minutes of your time**, and the tooling for stage 1
is built and tested:

```sh
switchnetctl hosts -bcat-probe > .../atmosphere/hosts/sysmmc.txt   # and emummc.txt
```

Open Splatoon 3, watch the stubs log, then turn the hosts flip back off. Whatever appears
— including nothing at all — is the first record anyone has. Write it up under
`docs/CAPTURES/`.

**Changes if answered:** `internal/nintendo/hosts.go` (the three `bcat-*`
entries), `TestBlackholedHostsAreAccountedFor`, and whether
`switch/bcat-installer/` is needed at all.

---

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
`IsValid: false` -- SwitchNet has no real battle-outcome data to compute a
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

## What does `CreateFestEntry`'s response actually name a new entry?

`festEntryProto` builds `tenants/<t>/fests/<ID>/entries/<userID>`, extending
`GetFestDecryptionKey`'s already-confirmed `tenants/<t>/fests/<ID>/...`
prefix with an `entries/<userID>` suffix that itself is not confirmed by any
capture. `GetFestEntry`/`GetFestResult`'s own names follow the same pattern
for the same reason -- consistency with an established convention, not new
evidence.

**Proceeding on:** the extended prefix, parsed by
`festEntryIDsFromResourceName` the same lenient way `lastSegment` parses
NPLN's other resource names (per `docs/CAPTURES/README.md`'s "already
guessed" list) -- a wrong suffix costs a cosmetic mismatch, not a broken
`Create`/`Get`.

**Changes if answered:** `internal/toyohr/entries.go`'s `festEntryProto`
and `festEntryIDsFromResourceName`.

---

## Does Splatoon 3's boot sequence need anything from vermillion at all?

`internal/nintendo/hosts.go` classifies `gw.hac.lp1.vermillion.srv.nintendo.net`
as "NPLN device initialisation and account config", contacted before
penne. But the only public documentation for this host
([kinnay's wiki](https://github.com/kinnay/NintendoClients/wiki/Vermillion-Server))
describes it as the **virtual game card** system — device-transfer
licensing, a general Switch feature with nothing Splatoon-specific about
it — and gives no field-level schema for either endpoint `internal/nplnrest`
now answers.

Two readings are both consistent with what is known:

1. Vermillion really is an NPLN prerequisite on real hardware (the reading
   `hosts.go`'s existing classification asserts, from evidence this pass
   could not re-derive), and the wiki's "virtual game cards" description is
   an older or incomplete account of a host that has since grown an
   NPLN-adjacent purpose.
2. Vermillion is contacted for unrelated general-account-management
   reasons on the normal boot path of any title, and has no bearing on
   whether Splatoon 3's own gRPC channel opens successfully.

**Proceeding on:** `hosts.go`'s existing classification, unchanged — this
pass did not have grounds to override it either way. `internal/nplnrest`
answers vermillion's two named endpoints as honestly-empty JSON
(`internal/nplnrest/README.md` has the detail) rather than leaving the host
unanswered, since answering costs nothing if the reading is (2) and avoids
a hang if it is (1).

**Changes if answered:** if (2), `gw.hac.lp1.vermillion.srv.nintendo.net`
moves out of `ServiceNPLNRest` in `internal/nintendo/hosts.go` (probably to
`ServiceStubs`, alongside `god`) and `internal/nplnrest` drops its two
vermillion handlers entirely. If (1), the two handlers gain the real
response fields a capture would reveal.

---

## How does a console obtain a Penne id and password?

`internal/nplnrest`'s `POST /v1/login_tickets` accepts whatever `id` and
`password` a console presents, without verifying them (CLAUDE.md's second
ground rule — this is not a credential SwitchNet issued). What is missing
is how a fresh console gets one to present in the first place: the wiki
names two `god`-server endpoints that plausibly create one
(`POST /v1/immigrate`, `POST /v1/penne_ids`) but marks their HTTP method
as unconfirmed, and `god.hac.lp1.penne.srv.nintendo.net` is not served by
`internal/nplnrest` at all — `internal/nintendo/hosts.go` still classifies
it `ServiceStubs`, answering it generically rather than with anything
Penne-shaped.

**Proceeding on:** nothing needs to, yet — accepting any presented id
without a provisioning step means a console can always obtain a login
ticket even though nothing ever formally issued it a Penne identity. This
is honest about what is unknown rather than a gap in behaviour: SwitchNet
does not need to recognise a *returning* Penne id, only to answer the
request in front of it.

**Changes if answered:** `god.hac.lp1.penne.srv.nintendo.net` would need to
move from `ServiceStubs` to `ServiceNPLNRest` in
`internal/nintendo/hosts.go`, and `internal/nplnrest` would gain handlers
for whichever of `/v1/immigrate`/`/v1/penne_ids` turns out to be the real
provisioning call.

**A weaker, unconfirmed lead, 2026-09-05:** `NextendoNetwork/nx-dauth` (a
from-scratch dauth/aauth reimplementation tested against real hardware;
PolyForm Shield-licensed, so only this fact is taken, never its code)
serves `POST /v2/penne_id` from `dragons.hac.lp1.dragons.nintendo.net` —
not `god` at all — answering `{"id": "nxd<uuid>", "password": "<24 random
bytes>"}`. Its own source carries no comment tying this to an observed
console request, unlike the `elicenses/exercise` finding recorded in
`docs/CAPTURES/README.md`'s item 10, so this is a lead to check before
spending a capture on `god`, not a settled answer. If it holds up, `god`
may be the wrong host entirely and this question's "changes if answered"
above would target `internal/stubs`'s `dragons` handling instead of
`internal/nplnrest`.

---

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

Today it is a shared token (`dashboard.token`), and unset there is none. That is
deliberate — nothing in phase 1 authenticates a *person*, only consoles — but it
means anyone reachable can schedule and cancel Splatfests.

**Proceeding on:** the shared token. A bespoke user system for one page would
likely be thrown away when phase 3's Nintendo Account work introduces the first
real human login.

**Changes if answered:** `internal/dashboard/server.go` (`guard`).
