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

The container is a 4-byte little-endian length followed by block-padded
ciphertext. The length bounds the real content, so there is no padding scheme to
strip; trusting the block size instead hands zstd up to fifteen bytes of rubbish.

## Texture conversion: shelling out, not reimplementing

`ConvertTextures` (`texture.go`) turns a directory of PNGs into the `.bntx`
files `BuildPacks` expects, by shelling out to an external tool rather than
reimplementing BCn compression and Tegra swizzling in Go -- TODO.md §2.1
says why: getting swizzling wrong "produces a texture that loads and renders
as garbage, which is worse than one that fails," and an existing,
purpose-built tool is a smaller risk than a fresh reimplementation.

**What was actually verified, not assumed:** the tool this was built and
tested against is
[jam1garner/img2bntx](https://github.com/jam1garner/img2bntx) (and the
`bntx` crate it wraps). It was cloned, built from source with `cargo build
--release`, and run end to end against a real PNG as part of writing this
package -- not taken on faith from its README. The output was byte-inspected
and confirmed to carry a real `BNTX`/`NX  ` header and the given name in its
string table.

Two real properties of that output, read from its source rather than
guessed: every texture comes out **uncompressed R8G8B8A8_SRGB** (no
BC1/BC3/BC7 -- larger files, but nothing to get wrong about a compression
format), and the container holds **exactly one texture, one mip level, no
array layers**. It has no releases or prebuilt binaries; building it needs a
Rust toolchain (`cargo build --release` from a clone, or
`cargo install --git https://github.com/jam1garner/img2bntx`).

`ConvertTextures` calls whatever binary `-texture-converter` names
(`switchnetctl fest build`'s own flag) using img2bntx's CLI contract
(`<tool> <in.png> <out.bntx> -n <name>`) -- any tool that speaks the same
contract works, this package does not hardcode img2bntx specifically.
Nothing here vendors the Rust source: it stays an external, operator-built
dependency, the same way this repository treats mitmproxy for packet
captures rather than embedding a proxy.

**Settled 2026-09-03, against a real fest pack (`JUEA-00210`), not
guessed:** a Splatfest does not load a standalone `.bntx`. Its `base` pack
carries exactly one visual asset, `Model/Fest_JUEA-DUMPLING.bfres.zs` -- an
`FRES` container (Switch/NX BFRES, magic `"FRES    "`, not the Wii U-era
`v3.x` format most public BFRES libraries target) with **one embedded BNTX
texture archive holding five named textures** (`M_HappiA_Alb`,
`M_HappiB_Alb`, `M_HappiC_Alb`, `IconA_Uia`, `IconB_Uia` -- three team happi
recolors and two icons) and no model data of any kind (no `FMDL`/`FVTX`
section anywhere in the file). `ConvertTextures`'s bare `.bntx` output is
therefore not, on its own, what a console loads for this.

Two things worth recording precisely, since a future pass will need them:
the embedded BNTX's own header (revision `0x400c`, the `BNTX`/`NX  ` layout)
matched jam1garner's `bntx` crate **byte for byte** -- direct, real
confirmation that this project's earlier verification of img2bntx's output
shape was correct, not just plausible. What is *not* settled: the outer
FRES container's own pointer/dictionary structure (the multi-texture
dictionary the embedded BNTX uses, and roughly 4KB of outer-FRES data this
pass could not identify) was not fully reverse-engineered, because doing so
with confidence needs either a public spec this pass did not find or
hardware to test a wrong guess against, and CLAUDE.md's first ground rule
means the real file this was checked against cannot be committed here as a
fixture regardless. **Do not build FRES-authoring code from a partial
understanding of that structure** -- the failure mode is a texture that
loads and renders as garbage, per TODO.md §2.1's own warning, with no way
for this project to test it before an operator does.

The best-founded next step, if this is picked up again: same-dimension
texture *replacement* inside an existing real template (an operator-local
file, never committed -- the same pattern as `SWITCHNET_BCAT_KEY_MATERIAL`
below), splicing new swizzled pixel bytes into an already-valid container
without changing its size or touching any pointer. That is a materially
smaller, safer problem than authoring a new container's dictionary and
relocation table from scratch, and was not attempted this pass for the same
reason: it still needs the multi-texture dictionary walked correctly to
find the right splice point, which was not verified with confidence.

BFRES *model* authoring and BYAML editing (team names, idol dialogue) remain
completely untouched either way.

## The lookup table is not in this repository

`nisasyst`'s IV table is lifted from the game binary, so under CLAUDE.md's first
rule it is not committed here. Point `SWITCHNET_BCAT_KEY_MATERIAL` at a file
holding it; without it every function that needs an IV fails saying exactly what
is missing, rather than producing packs that silently do not open.

The tests use a **synthetic** table, which exercises the identical code. The
implementation was separately verified against the reference tooling on the
*real* table: all nine derivations matched byte for byte, empty path included.
That check cannot live in the test suite without committing the table.
