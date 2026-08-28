# switchnet-bcat-installer

A homebrew that fetches a Splatfest's BCAT delivery cache from SwitchNet and
writes it into Splatoon 3's BCAT save data — what JKSV does by hand,
automatically.

> **This has never been compiled.** There was no devkitPro toolchain in the
> environment it was written in — no `DEVKITPRO`, no `aarch64-none-elf-gcc` — so
> `source/main.c` and the `Makefile` are source that has not been built, let
> alone run on hardware. Treat every line as a proposal. Saying so is more useful
> than implying otherwise: untested C that writes into save data is not something
> to run on a console you care about without reading it first.

## Why it exists

SwitchNet builds the delivery cache rather than speaking BCAT's delivery
protocol, because that protocol's `list` response body is undocumented and no
public implementation exists — see [`internal/bcat`'s
README](../../internal/bcat/README.md), and `TODO.md` §2.4 for the experiment
that may change that.

So the files have to get onto the console some other way. By hand with JKSV works
and does not survive the second fest.

**If §2.4's experiment comes back positive, this may become unnecessary.** It
exists because that question is open, not because serving BCAT is forbidden.

## Setup

Build it, then put two things on the SD card:

```
/switch/switchnet-bcat-installer/switchnet-bcat-installer.nro
/switch/switchnet-bcat-installer/switchnet.cfg
```

`switchnet.cfg` is two lines:

```
url=https://192.168.1.50:8087
token=whatever-dashboard.token-is
```

The URL is the **dashboard's**, because that is what serves the seed
(`internal/dashboard/seed.go`), and `bcat.seed_dir` has to point at wherever
`switchnetctl fest build -out` wrote it.

A config file rather than a compiled-in address, because rebuilding a homebrew to
change a hostname is not a workflow.

## What it does, in order

1. reads `switchnet.cfg`
2. `GET /api/seed/manifest` — which files, their sizes and their SHA-1s
3. **mounts the save data before downloading anything**, so a console that cannot
   mount it does not spend two minutes fetching first
4. per file: `GET`, check the length, check the digest, write
5. `fsdevCommitDevice` — without which the writes are discarded on unmount,
   silently, and the install appears to have worked

It stops on the first failure. A half-written cache means the console holds some
of a fest's packs and answers `BcatInvalid` on the rest — hours later, in a
lobby, with nothing connecting the two events.

## Three details worth keeping

**The digest is checked before the write, not after.** A truncated pack is
indistinguishable from a valid one to the filesystem.

**TLS verification is off, explicitly and in one place.** SwitchNet's certificate
is signed by the operator's own CA, which the console has no reason to trust and
no way to be told about from inside a homebrew. The connection is to the
operator's own server on the operator's own network, so verification buys
nothing — but it is disabled with a comment saying why, rather than by a flag
somebody finds later and cannot explain. Use `http://` if you would rather have
no TLS than unverified TLS.

**The title id is Splatoon 3's.** BCAT save data is per title, so a wrong id
mounts a different game's cache — which succeeds, and then does nothing visible.

## Building

```sh
sudo dkp-pacman -S switch-dev switch-curl switch-mbedtls
make
```

## If you would rather not run untested C

The manual route still works, and the endpoints this uses are ordinary HTTP:

```sh
curl -H "Authorization: Bearer $TOKEN" https://server:8087/api/seed/manifest
curl -H "Authorization: Bearer $TOKEN" -O \
  https://server:8087/api/seed/file/eu-default/base.pack.zs.enc
```

Then put the files into Splatoon 3's BCAT save data with JKSV, keeping the
`<region>-default/` layout.
