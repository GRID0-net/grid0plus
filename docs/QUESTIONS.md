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

**Proceeding on:** `npln.ice_servers` in config, empty by default, with a
`switchnetctl check` warning when unset. Matchmaking works for peers on the same
LAN without it. **This is built** — `AllocateIceServerSet` serves whatever the
list holds, and an empty list means no ICE servers are offered.

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

**Proceeding on:** names are inert — packs are built with whatever names the
source assets carry. This is the cheaper assumption and it is what existing
custom fests do, but it is unverified.

**Changes if answered:** `internal/bcat/build.go`, and TODO §2.1's scope.

---

## Do sigpatches cover the `bcat` sysmodule?

See TODO §2.4 for the full experiment. If they do, serving BCAT natively may be
possible and the `.nro` installer (§2.4a) becomes unnecessary. `sys-patch`
covers `fs`, `es`, `ldr` and `nifm`; `bcat` is not on that list.

**Proceeding on:** it is not covered, so SwitchNet builds the delivery cache and
does not serve BCAT. Note that this is only *half* the blocker — the `list`
response body is undocumented regardless.

**The experiment is now five minutes of your time**, and the tooling for stage 1
is built and tested:

```sh
switchnetctl hosts -bcat-probe > .../atmosphere/hosts/sysmmc.txt   # and emummc.txt
# stubs.bcat_probe: true
```

Open Splatoon 3, watch the stubs log, then turn both back off. Whatever appears
— including nothing at all — is the first record anyone has. Write it up under
`docs/CAPTURES/`.

**Changes if answered:** `internal/nintendo/hosts.go` (the three `bcat-*`
entries), `TestBlackholedHostsAreAccountedFor`, and whether §2.4a is built at
all.

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

**Changes if answered:** `internal/toyohr/fest_entries.go`'s `GetFestResult`.
If the console needs a genuinely valid result, the entry-count-derived ratio
this pass rejected becomes the next thing to reconsider -- explicitly, in a
commit that says why it is no longer considered misleading enough to avoid.

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

**Changes if answered:** `internal/toyohr/fest_entries.go`'s `festEntryProto`
and `festEntryIDsFromResourceName`.

---

## How should the dashboard authenticate a person, long term?

Today it is a shared token (`dashboard.token`), and unset there is none. That is
deliberate — nothing in phase 1 authenticates a *person*, only consoles — but it
means anyone reachable can schedule and cancel Splatfests.

**Proceeding on:** the shared token. A bespoke user system for one page would
likely be thrown away when phase 3's Nintendo Account work introduces the first
real human login.

**Changes if answered:** `internal/dashboard/server.go` (`guard`).
