# internal/bcat

Builds the BCAT packs a custom Splatfest's content lives in — the team names,
banners and idol dialogue that `internal/toyohr`'s schedule points at but does
not carry.

| | |
| --- | --- |
| Reference | [BCAT Content Container](https://switchbrew.org/wiki/BCAT_Content_Container), [BCAT Servers](https://github.com/kinnay/NintendoClients/wiki/BCAT-Servers) |
| Needs a database | no |

## This does not serve BCAT yet — and the blocker is now known exactly

GRID0+ builds the delivery cache directly rather than speaking BCAT's
delivery protocol. As of 2026-09-26 the reason is no longer "nobody has
documented how"; both unknowns have been settled, one from a public client
of the live servers and one from disassembling the `bcat` sysmodule itself.

**The container format is known.** The `list` body is itself an encrypted
`bcat` container whose payload is MessagePack; the 0x120-byte header layout
and the key derivation are in TODO.md §2.4. Serving the manifest is no
longer inventing a shape.

**The signature is verified, and that is the blocker.** `bcat.nso`
(22.5.0) runs an RSA-2048 verify over each decrypted container's payload
against a public key baked into the module, and on failure wipes the buffer
and returns result 2002-4106 — read directly from the disassembly, not
guessed from switchbrew's note that the field exists. So a
GRID0+-produced container is refused unless `bcat.nso` is patched to
force the verify to pass. That is the same class of patch this project
already ships for Splatoon 3's TLS (an ExeFS/IPS patch, built with
borntohonk's pattern tooling against the exact firmware build), and it is
*not* forging Nintendo's signature — rule 2 stays intact. `sys-patch`
covers `fs`, `es`, `ldr` and `nifm`, not `bcat`, so this needs its own
patch shipped alongside GRID0+'s others.

**Encryption is not a blocker.** The header validator accepts crypto type 0
(plaintext), so a served container needs no passphrase and none of the
module's secret strings; only the signature does.

So: serving BCAT natively is possible, gated on one `bcat.nso` patch per
firmware build plus the `list`/`data` responses. TODO.md §2.4 tracks it.
Building the cache directly still sidesteps the patch entirely, which is why
it remains what this package does today — but §2.4a's homebrew installer is
now a fallback, not the only option.

## The probe, and why it exists before the experiment does

`Probe` is the server for TODO.md §2.4 stage 1: *what does the console ask
BCAT for?* The three `bcat-*` hosts reach `internal/stubs`, which answers 304
("your cache is current"; anything else risked Splatoon 3's 2122-2403). With
`stubs.bcat_record_dir` set, stubs also hands each request to
`Probe.Observe`, which logs and records it, hexdumped, without changing the
answer. One boot gives the list/data paths native serving must answer.

`Probe.ServeHTTP` — answering the request itself — is kept for a later stage,
when there is a real container to serve.

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

## Source assets, and the one thing this pipeline refuses to do

An assets directory may hold two kinds of file.

**Already in a game format** (`.bfres`, `.byml`, `.sarc.zs`) — packed as it is.

**A source file**, named for what it must become with the source extension
appended:

```
UI/Splashart.bfres.png    ->  UI/Splashart.bfres
Mals/Msg_EUen.byml.yaml   ->  Mals/Msg_EUen.byml
```

One line, no separate manifest to keep in step with the filenames, and the
target format visible in a directory listing.

**A source file with no converter configured is a build error.** That refusal is
the most valuable thing in this file. Before it, a PNG left in an assets
directory went straight into the archive — the archive is still valid, so the
console reads it, and then cannot decode the texture slot. The result is a fest
that loads with missing or garbled art, on a console, with nothing on the server
to look at. Failing at build time costs one message.

## GRID0+ does not convert textures, on purpose

A Splatfest's art is BC1/BC3/BC7-compressed textures in the Tegra GPU's
block-linear layout, wrapped in BFRES. Getting any of that subtly wrong produces
a texture that **loads and renders as garbage** — not a build error, not a
refusal, noise on screen. Switch Toolbox and the
`BinaryTextureLibrary`/`Syroot.NintenTools` family already do it correctly, and
they exist because it is hard.

So `bcat.converter` is a command the operator points at whatever tool they use:

```
<command> <source> <destination> <kind> <target-extension>
```

It must write the destination and exit zero. A non-zero exit, a missing output
or an **empty** output all stop the build, carrying the command line that
produced them — a conversion that half-worked is far harder to diagnose from the
resulting pack than from the command that made it. The command is resolved when
it is configured rather than when it is used, so a missing tool fails at startup
and not halfway through building a fest.

Two smaller guards, each closing a way to ship a broken pack:

- **collisions are refused.** `Splashart.bfres.png` alongside `Splashart.bfres`
  would otherwise be stored twice under one name, and the console reads whichever
  it finds first.
- **converted output never lands in the assets directory.** It goes to a scratch
  directory that is removed, so the next build does not treat it as
  hand-authored input.

## `{FEST}` — one place for a question nobody has answered

A real custom pack read during this project's research had every file inside
named for the fest it was **derived from**, not the one it announces:

```
Mals/Msg_EUde.Fest_JUEA-DUMPLING_Bh.sarc.zs
RSDB/Tag.Fest_JUEA-DUMPLING.dev.rstbl.byml.zs
```

while its `summary.yml` announced `JUEA-00210`. **Nobody has established what the
game keys on.** If it resolves assets by the announced id, a from-scratch fest
needs every internal path renamed consistently; if it only reads the pack it was
handed, the names are inert.

`Naming.Apply` is that decision in one place. Write
`Mals/Msg_EUen.Fest_{FEST}_Bh.sarc.zs` and get consistent naming; write a literal
name and it is left alone. Whichever way the question is answered, acting on the
answer is a change to one function.

With no fest id supplied the placeholder is left **visible** rather than
stripped: a file literally called `Fest_{FEST}_Bh` is obviously wrong in a
listing, whereas `Fest__Bh` looks like a real name and would be shipped.

## Building one

`switchnetctl fest build` is the only path that should be used, because it derives
all four of the values that must agree from one source: the packs' bytes give the
revision, the generated keys go to both the encryption and the fest row, and the
announced id and resource are the ones written into `summary.yml`.

`TestASeedAndItsKeysRoundTrip` is that contract as a test: it builds, writes, and
then does exactly what a console does — takes the key `GetFestDecryptionKey` would
hand over, derives the IV from the announced resource name, decrypts, and parses
the archive.

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

**Also settled 2026-09-03, against
[`RAMDRAGONS/Splatoon-FesData`](https://github.com/RAMDRAGONS/Splatoon-FesData)
(37 independent real fest packs, cross-checked against `JUEA-00210` above):
what compression format each real texture slot actually uses, and that
img2bntx's uncompressed output is a bigger gap than "larger files."** Every
happi-coat and signboard slot is `0x1a06`; every icon slot is `0x2006`. That
looks like it should mean BC7 for the small icons and something lighter for
the large happi coats -- backwards from what "larger asset, lighter format"
would suggest, which is exactly the kind of guess CLAUDE.md rule 6 warns
against making from a plausible-looking pattern. Read against
[`ScanMountGoat/bntx`](https://github.com/ScanMountGoat/bntx)'s own
`SurfaceFormat` enum (`src/lib.rs`) instead of guessed: **`0x1a06` is
`BC1Srgb`, `0x2006` is `BC7Srgb`** -- happi coats and the signboard are BC1,
the icons are BC7, the reverse of the size-based guess. (Not ASTC anywhere;
this project has no reason to think Splatfest textures use it.)

Verified end to end, not just read from source: built
[`ScanMountGoat/ultimate_tex`](https://github.com/ScanMountGoat/ultimate_tex)'s
`ultimate_tex_cli` from source (`cargo build --release`, needing
`RUSTFLAGS="-C link-arg=-lstdc++"` -- its ASTC backend links a compiled
ISPC/C++ object the Rust linker does not pull `libstdc++` in for on its
own) and ran it against synthetic images matching each real slot's exact
dimensions:

| slot | real dimensions | real format | real mips | `-f` flag that reproduced both, byte for byte |
| --- | --- | --- | --- | --- |
| happi coat | 512×512 | `0x1a06` | 10 | `BC1RgbaUnormSrgb` |
| signboard | 870×340 | `0x1a06` | 10 | `BC1RgbaUnormSrgb` |
| icon | 64×64 | `0x2006` | 7 | `BC7RgbaUnormSrgb` |

The mip counts needed no flag at all -- `ultimate_tex_cli`'s default
(`Mipmaps::GeneratedAutomatic`, full chain down to 1×1) lands on 10 and 7
respectively for these exact dimensions, matching the real data without
`--no-mipmaps` or any override.

This does not change what `ConvertTextures` ships: `ultimate_tex_cli`
takes the output name from the filename rather than an `-n` flag, and
picking the right `-f` per slot needs code that knows which named slot an
image is going into (happi/signboard vs. icon) -- and, per the section
above, an uncompressed standalone `.bntx` from either tool is not what the
game loads regardless of compression, since the real asset is the one
embedded in a `.bfres`'s `FRES` container. Recorded here as the exact,
verified mapping the next pass needs the moment the FRES-wrapper problem
is solved, so that pass does not have to re-derive it -- or repeat this
one's first guess, which had the two formats backwards.

## The lookup table is not in this repository

`nisasyst`'s IV table is lifted from the game binary, so under CLAUDE.md's first
rule it is not committed here. Point `SWITCHNET_BCAT_KEY_MATERIAL` at a file
holding it; without it every function that needs an IV fails saying exactly what
is missing, rather than producing packs that silently do not open.

The tests use a **synthetic** table, which exercises the identical code. The
implementation was separately verified against the reference tooling on the
*real* table: all nine derivations matched byte for byte, empty path included.
That check cannot live in the test suite without committing the table.
