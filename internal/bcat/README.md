# internal/bcat

Builds the BCAT packs a custom Splatfest's content lives in — the team names,
banners and idol dialogue that `internal/toyohr`'s schedule points at but does
not carry.

| | |
| --- | --- |
| Reference | [BCAT Content Container](https://switchbrew.org/wiki/BCAT_Content_Container), [BCAT Servers](https://github.com/kinnay/NintendoClients/wiki/BCAT-Servers) |
| Needs a database | no |

## This does not serve BCAT — but the reason is narrower than it looks

SwitchNet builds the delivery cache directly rather than speaking BCAT's
delivery protocol. Two reasons have been given for that at different times, and
only one of them holds:

**The reason that does NOT hold on its own.** Nintendo's delivery container
carries an RSA-2048 signature at offset 0x20 of its 0x120 header. It is
tempting to conclude the console verifies it and that producing one would mean
forging a Nintendo signature — but switchbrew documents only that the *field
exists*. It says nothing about a verification step, nothing about what happens
on failure, and no public source establishes either. More to the point, a
console running SwitchNet already has sigpatches: bypassing Nintendo signature
checks is the normal state of a CFW console, and `ROADMAP.md` §3.3 already
depends on exactly that for phase 3. Whether those patches reach the `bcat`
sysmodule is untested — `sys-patch` covers `fs`, `es`, `ldr` and `nifm`, and
`bcat` is not on that list — but "untested" is not "impossible".

**The reason that does hold.** The response body of
`/api/nx/v1/list/<name>` is undocumented. Kinnay's wiki gives the paths and
nothing else; no public implementation exists. Serving BCAT means inventing the
manifest the console parses, and a wrong manifest gives a console that
connects, receives something unparseable, and fails with nothing in any log —
which is CLAUDE.md rule 6, not rule 2.

So: **not "we cannot", but "nobody has documented how".** TODO.md §2.4 has the
experiment that would change this. If it comes back positive, revisit properly
rather than treating this paragraph as settled.

Building the cache directly sidesteps the question entirely, which is why it is
what this package does today.

## The probe, and why it exists before the experiment does

`Probe` is the server for TODO.md §2.4 stage 1: *does the console reach us at
all?* It logs every request the three `bcat-*` hosts receive, writes a hexdumped
transcript when `stubs.bcat_record_dir` is set, and answers 200 with an empty
body.

It is wired into `internal/stubs` and **unreachable in a normal deployment**,
because those three hosts are null-routed. Running the experiment is:

1. change the three `bcat-*` entries in `internal/nintendo/hosts.go` from
   `ServiceBlackhole` to `ServiceStubs`;
2. move them in `TestBlackholedHostsAreAccountedFor`'s map, in a commit that
   says why;
3. regenerate the DNS and nginx configuration, set `stubs.bcat_record_dir`, and
   open Splatoon 3.

That is deliberately a five-minute job. The scarce resource in this experiment is
console time, not server code, so the server exists first.

**An empty 200 is a decision, not a placeholder.** An error tells the console the
server is broken and it may stop asking, which loses the only thing the probe
collects. A fabricated container is worse: the console rejects it for a reason
nobody can name, and a negative result then means nothing.

**Stage 2 is not built, and cannot honestly be.** Answering with a deliberately
invalid signature only tests whether the signature is checked if the rest of the
header is right — and the only public fact about that header is its size and the
signature's offset. Stage 1's hexdumps are how the rest becomes known. Building
stage 2 from a guess would produce an experiment whose negative result is
uninterpretable.

## What it produces

```
bcat-seed/<region>-default/
    summary.yml            FestID, FestResourceID, FestResourceRevision
    base.pack.zs.enc       the fest's main content
    bh.pack.zs.enc         battle help
    wa/wb/wc.pack.zs.enc   one per team
```

Layout, filenames and field names all confirmed against a real seed folder a
working server produced (JUEA-00210, 2026-08-24), whose `summary.yml` is:

```yaml
FestID: JUEA-00210
FestResourceID: juea-lovefes2
FestResourceRevision: a587beaaaf515afda77d35901c62038c8484a042
```

That file is the contract with `internal/toyohr`: the announced schedule must
carry these three values exactly, or the game answers `BcatInvalid`.

**The resource is a hand-picked slug, not a derived name.** `juea-lovefes2`
against fest `JUEA-00210` — nothing links them. `toyohr.NewResource` only names
a pack nobody has built yet; once a pack exists, its `summary.yml` is the
authority and the schedule follows it.

## nisasyst

The packs are AES-128-CBC. The key is whatever `GetFestDecryptionKey` hands
over — the server's to choose — and the **IV is derived from the pack's resource
path**: CRC-32 of the path seeds `sead::Random`, and each of the sixteen IV
bytes is two hex digits drawn from a lookup table at the index in the top byte
of successive 32-bit draws.

Both inputs being ours is precisely what makes a genuinely custom fest possible
rather than a rename of one of Nintendo's.

Two consequences worth stating, each with a test:

- **The resource name is case-sensitive in a way nothing about it suggests.**
  One wrong capital changes the IV; the pack does not open; the console reports
  a generic communication error naming nothing.
- **The base pack is named differently from every other pack.** Its IV path is
  the resource alone (`juea-lovefes2.pack.zs`); the others append their short
  name (`juea-lovefes2_bh.pack.zs`). Getting it wrong breaks exactly one of the
  five — the largest.

## Five packs, six key fields

A seed folder holds **five** packs. `FestDecryptionKey` declares **six** key
fields, and the keys are returned *positionally* — so no positional mapping
between the two can be right, and treating `PackOrder` as a key order was a real
bug: `wa`'s key landed in `result_winning_team`, `wb`'s in `team_alpha`, and every
team pack failed to open with nothing in the failure naming a phase.

`KeyFields` and `PackForKeyField` are the mapping, and `KeysInFieldOrder` is the
only place that turns per-pack keys into the stored list.

The three team fields are the certain half — `team_alpha` opens `wa` and so on;
nothing else reads sensibly. Which of `notice` and `start` opens `base` and which
opens `bh`, and what `result_winning_team` opens at all, is
`TODO(capture-needed)`: a real seed folder has five files and the message has six
fields, so at least one field has no pack.

An absent pack keeps an **empty slot** rather than being left out. A two-team
fest has no `wc`, and dropping its slot shifts every later key into the wrong
phase.

The container is a 4-byte little-endian length followed by block-padded
ciphertext. The length bounds the real content, so there is no padding scheme to
strip; trusting the block size instead hands zstd up to fifteen bytes of rubbish.

## The lookup table is not in this repository

`nisasyst`'s IV table is lifted from the game binary, so under CLAUDE.md's first
rule it is not committed here. Point `SWITCHNET_BCAT_KEY_MATERIAL` at a file
holding it; without it every function that needs an IV fails saying exactly what
is missing, rather than producing packs that silently do not open.

The tests use a **synthetic** table, which exercises the identical code. The
implementation was separately verified against the reference tooling on the
*real* table: all nine derivations matched byte for byte, empty path included.
That check cannot live in the test suite without committing the table.
