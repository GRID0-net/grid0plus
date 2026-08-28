# internal/bcat

Builds the BCAT packs a custom Splatfest's content lives in — the team names,
banners and idol dialogue that `internal/toyohr`'s schedule points at but does
not carry.

| | |
| --- | --- |
| Reference | [BCAT Content Container](https://switchbrew.org/wiki/BCAT_Content_Container), [BCAT Servers](https://github.com/kinnay/NintendoClients/wiki/BCAT-Servers) |
| Needs a database | no |

## This does not serve BCAT, and cannot

Nintendo's BCAT delivery protocol wraps its payload in a container whose 0x120
header carries an **RSA-2048 signature at offset 0x20**, with the verifying
modulus selected by the console's production-mode setting. Producing a container
the console accepts means signing it as Nintendo.

That is refused on two grounds, and the second is the one that settles it:

1. we do not have the private key, so we could not anyway;
2. **forging it is exactly what CLAUDE.md's second rule forbids** — "never
   verify a Nintendo-issued credential and never forge one". A signature minted
   to pass Nintendo's verification is a forged Nintendo credential.

Two independent projects reached the same place from better information:
Nextendo ships a `bcat-seed` zip for people to install by hand, and NeXo-Server
stubs an emulator-only protocol. Neither serves real BCAT.

So this package builds **the delivery cache directly** — the files a real BCAT
download would have left behind — and they are installed onto the console by
other means.

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

## The lookup table is not in this repository

`nisasyst`'s IV table is lifted from the game binary, so under CLAUDE.md's first
rule it is not committed here. Point `SWITCHNET_BCAT_KEY_MATERIAL` at a file
holding it; without it every function that needs an IV fails saying exactly what
is missing, rather than producing packs that silently do not open.

The tests use a **synthetic** table, which exercises the identical code. The
implementation was separately verified against the reference tooling on the
*real* table: all nine derivations matched byte for byte, empty path included.
That check cannot live in the test suite without committing the table.
