# bcat

BCAT is how a console downloads content such as Splatfest packs. This package builds those packs for a custom Splatfest and
serves them to consoles from GRID0+'s own `bcat-list` and `bcat-data` hosts.

| | |
| --- | --- |
| Reference | [BCAT Content Container](https://switchbrew.org/wiki/BCAT_Content_Container), [BCAT Servers](https://github.com/kinnay/NintendoClients/wiki/BCAT-Servers) |
| Needs a database | no |

The operator procedure is in [SPLATFESTS.md](../../docs/SPLATFESTS.md). This page explains how the package works.

## Serving BCAT to consoles

`internal/stubs` answers the BCAT list and data paths when `bcat.seed_dir` points at a directory laid out per title:

```text
<seed_dir>/0100c2500fc20000/eu-default/<filename>   Splatoon 3, Europe
<seed_dir>/0100c2500fc20000/us-default/<filename>   Splatoon 3, Americas
<seed_dir>/0100c2500fc20000/jp-default/<filename>   Splatoon 3, Japan
<seed_dir>/0100c2500fc20000/ap-default/<filename>   Splatoon 3, Asia Pacific
<seed_dir>/01003bc0000a0000/vsdata/<filename>       Splatoon 2, Americas
<seed_dir>/0100f8f0000a2000/vsdata/<filename>       Splatoon 2, Europe
<seed_dir>/01003c700009c000/vsdata/<filename>       Splatoon 2, Japan
```

Each immediate subdirectory of a title is one BCAT delivery-cache directory, and the regular files in it are advertised and served. The
server never invents game data. A title with no files keeps answering 304 ("nothing new"), and so does the system news host.
The console's BCAT parser reports error `2122-0002` when the directory it asks for is missing from the list, so the regional names above
matter.

**The format the console expects.** The list is itself a `bcat` container whose payload is MessagePack. The container has a 0x120-byte
header (magic `bcat`, a crypto type, a hash type, a secret index, an IV and a 0x100-byte RSA signature) followed by the payload. GRID0+
serves containers with crypto type 0 (plaintext), which the console accepts, so no passphrase or secret strings are needed. The
`mode` of a `data_list` entry must be `sync` or `copy`; `async` fails with `2122-0203`. A `data_list` entry also needs `version.format`,
`version.semantics` and `news_id` when it closes, and the builder sends zero for those three values.

**The signature.** The `bcat` system module verifies the container's RSA signature and refuses content that fails. GRID0+ can't sign
for Nintendo, so consoles run a firmware patch that skips the check (`switch/atmosphere/exefs_patches/bcat_signature_bypass/`; see
[CONSOLE_SETUP.md](../../docs/CONSOLE_SETUP.md#bcat-on-firmware-2250)). The patch matches one firmware build, so a firmware update needs a
new one. Without it, consoles can be given the cache directly instead, through the installer in `switch/bcat-installer/`.

`Probe` is a diagnostic. With `stubs.bcat_record_dir` set, every request to a `bcat-*` host is also written to disk, hexdumped, without
changing the answer. `Probe.ServeHTTP` is the handler that answers with an empty `200`, used only when you call it directly.

## Source assets

An assets directory may hold two kinds of file.

- **Already in a game format** (`.bfres`, `.byml`, `.sarc.zs`), which is packed as it is.
- **A source file**, named for what it must become with the source extension appended:

  ```
  UI/Splashart.bfres.png    ->  UI/Splashart.bfres
  Mals/Msg_EUen.byml.yaml   ->  Mals/Msg_EUen.byml
  ```

  The target format is visible in a directory listing and no separate manifest has to be kept in step.

**A source file with no converter configured is a build error.** Without that, a PNG left in an assets directory goes straight into the
archive. The archive is still valid, so the console reads it, and then can't decode the texture. You get a fest that loads with missing or
garbled art, on a console, with nothing on the server to look at.

## GRID0+ doesn't convert textures

A Splatfest's art is BC1, BC3 or BC7-compressed textures in the GPU's block-linear layout, wrapped in BFRES. Getting any of that subtly wrong
produces a texture that loads and renders as garbage instead of a build error. Switch Toolbox and the BinaryTextureLibrary family already
convert these correctly, so `bcat.converter` is a command you point at whatever tool you use:

```
<command> <source> <destination> <kind> <target-extension>
```

It must write the destination and exit zero. A non-zero exit, a missing output or an empty output all stop the build, with the command line
that produced them. The command is resolved when it is configured, so a missing tool fails at start-up and not halfway through a fest.

`ConvertTextures` (`texture.go`) turns a directory of PNGs into `.bntx` files by calling an external tool with the contract
`<tool> <in.png> <out.bntx> -n <name>`, such as [img2bntx](https://github.com/jam1garner/img2bntx). Its output is uncompressed
RGBA8, one texture and one mip level. A Splatfest doesn't load a standalone `.bntx`: its `base` pack carries one `FRES` container with an
embedded multi-texture BNTX archive, so a bare `.bntx` isn't what a console loads. Wrapping textures in that container isn't built. The
safest approach is replacing same-sized textures inside an existing template (kept outside the repository).

Two guards close ways of shipping a broken pack:

- **Collisions are refused.** `Splashart.bfres.png` beside `Splashart.bfres` would be stored twice under one name, and the console reads
  whichever it finds first.
- **Converted output never lands in the assets directory.** It goes to a scratch directory that is removed, so the next build doesn't treat it
  as hand-authored input.

## `{FEST}` in asset paths

Files inside a pack made from an existing fest carry that fest's id in their names. `Naming.Apply` is the one place that decides how: write
`Mals/Msg_EUen.Fest_{FEST}_Bh.sarc.zs` and the announced fest id is substituted in (`bcat.substitute_fest_id`, on by default), and a literal name is left
alone. With no fest id the placeholder stays visible, because a file literally called `Fest_{FEST}_Bh` is obviously wrong in a listing whereas
`Fest__Bh` looks real and would ship.

## Building a fest

`switchnetctl fest build` is the only path you should use, because it derives from one source the four values that must agree: the revision from the packs'
bytes, the generated keys for both the encryption and the fest row, and the id and resource written into `summary.yml`.
`TestASeedAndItsKeysRoundTrip` is that contract as a test: it builds a seed, writes it, then does what a console does (takes the key `GetFestDecryptionKey`
would hand over, derives the IV from the announced resource name, decrypts and parses the archive).

A seed folder looks like this, and `summary.yml` is the contract with [`internal/toyohr`](../toyohr/README.md):

```
bcat-seed/<region>-default/
    summary.yml            FestID, FestResourceID, FestResourceRevision
    base.pack.zs.enc       the fest's main content
    bh.pack.zs.enc         battle help
    wa/wb/wc.pack.zs.enc   one per team
```

```yaml
FestID: JUEA-00210
FestResourceID: juea-lovefes2
FestResourceRevision: a587beaaaf515afda77d35901c62038c8484a042
```

The announced schedule must carry these three values exactly, or the game answers `BcatInvalid`. The resource is a hand-picked slug and isn't derived from
the fest id; once a pack exists, its `summary.yml` is the authority and the schedule follows it.

## Pack encryption

Packs are zstd-compressed SARC archives inside AES-128-CBC. `Seed.WriteWith` compresses before it encrypts, and its round-trip test checks both layers.
The key is whatever `GetFestDecryptionKey` hands out, which is the server's to choose, and the **IV is derived from the pack's resource path**: the CRC-32 of the
path seeds a `sead::Random`, and each of the sixteen IV bytes is two hex digits drawn from a lookup table at the index in the top byte of successive 32-bit draws. Both
inputs being GRID0+'s own is what makes a custom fest possible.

Two consequences, each with a test:

- **The resource name is case-sensitive in a way nothing suggests.** One wrong capital changes the IV, the pack doesn't open, and the console reports a generic
  communication error.
- **The base pack is named differently from the others.** Its IV path is the resource alone (`juea-lovefes2.pack.zs`), and the others append their short name
  (`juea-lovefes2_bh.pack.zs`). Getting it wrong breaks exactly one of the five, the largest.

A seed folder can hold five packs while `FestDecryptionKey` has six key fields, so `PackOrder` isn't the protobuf field order. `KeyFields`, `PackForKeyField` and
`KeysInFieldOrder` keep the slots straight. An absent pack keeps an empty slot. A two-team fest has no `wc`, and dropping its slot would shift every later key into the
wrong phase.

The container is a 4-byte little-endian length followed by block-padded ciphertext. The length bounds the real content, so there is no padding scheme to strip.

**The IV lookup table isn't in this repository.** It comes from the game, so it is never committed. Point `SWITCHNET_BCAT_KEY_MATERIAL` at a file holding it, or install
it at `/etc/switchnet/bcat-key-material`. Without it, every function that needs an IV fails with a message saying exactly what is missing, instead of producing packs that
silently don't open. The tests use a synthetic table, which exercises the same code.
