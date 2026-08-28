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
address, and a guess there produces exactly the failure above. Matchmaking works
for peers on the same LAN without any of it.

**Changes if answered:** nothing in code — fill in `npln.ice_servers`.

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

**Changes if answered:** `internal/bcat.Naming.Apply`, and nothing else — the
naming decision was deliberately confined to that one function so that acting on
an answer is a one-line change rather than a rewrite.

---

## Do sigpatches cover the `bcat` sysmodule?

See TODO §2.4 for the full experiment. If they do, serving BCAT natively may be
possible and the `.nro` installer (§2.4a) becomes unnecessary. `sys-patch`
covers `fs`, `es`, `ldr` and `nifm`; `bcat` is not on that list.

**The server for stage 1 is written and waiting.** `internal/bcat.Probe` logs
every request the `bcat-*` hosts receive and writes a hexdumped transcript when
`stubs.bcat_record_dir` is set. Running the experiment is three lines in
`internal/nintendo/hosts.go` plus a config value — see TODO §2.4.

**Proceeding on:** it is not covered, so SwitchNet builds the delivery cache and
does not serve BCAT. Note that this is only *half* the blocker — the `list`
response body is undocumented regardless, and stage 1's transcript is what would
start to settle it.

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

## How should the dashboard authenticate a person, long term?

Today it is a shared token (`dashboard.token`), and unset there is none. That is
deliberate — nothing in phase 1 authenticates a *person*, only consoles — but it
means anyone reachable can schedule and cancel Splatfests.

**Proceeding on:** the shared token. A bespoke user system for one page would
likely be thrown away when phase 3's Nintendo Account work introduces the first
real human login.

**Changes if answered:** `internal/dashboard/server.go` (`guard`).
