# switchnet-bcat-installer

A homebrew that fetches a Splatfest's BCAT delivery cache from GRID0+ and writes it into Splatoon 3's BCAT save data. It does automatically
what JKSV does by hand.

> **It hasn't been compiled or tested.** The environment it was written in had no devkitPro toolchain, so `source/main.c` and the `Makefile` are
> source that has never been built or run on a console. Treat every line as a proposal. Untested C that writes into save data isn't something to run
> on a console you care about without reading it first.

## When to use it

Consoles normally download Splatfest content natively, through a patched `bcat` module (see [CONSOLE_SETUP.md](../../docs/CONSOLE_SETUP.md#bcat-on-firmware-2250)).
This installer is the fallback for a console that doesn't have the patch, for example on a firmware it doesn't cover yet. Copying files with JKSV also works, but
it doesn't scale past the first fest.

## Setup

Build it, then put two files on the SD card:

```
/switch/switchnet-bcat-installer/switchnet-bcat-installer.nro
/switch/switchnet-bcat-installer/switchnet.cfg
```

`switchnet.cfg` is two lines:

```
url=https://192.168.1.50:8087
token=whatever-dashboard.token-is
```

The URL is the dashboard's, because it serves the seed (`internal/dashboard/seed.go`), and `bcat.seed_dir` has to point at wherever
`switchnetctl fest build -out` wrote it. A config file keeps the address out of the binary, since rebuilding a homebrew to change a hostname isn't a workflow.

## What it does, in order

1. reads `switchnet.cfg`;
2. `GET /api/seed/manifest` to get the files, their sizes and their SHA-1s;
3. mounts the save data **before** downloading anything, so a console that can't mount it doesn't spend two minutes fetching first;
4. for each file, downloads it, checks the length, checks the digest and writes it;
5. calls `fsdevCommitDevice`, without which the writes are discarded on unmount, silently, and the install looks like it worked.

It stops on the first failure. A half-written cache means the console holds some of a fest's packs and answers `BcatInvalid` on the rest, hours later, in a lobby.

## Three details worth keeping

- **The digest is checked before the write, not after.** A truncated pack looks valid to the file system.
- **TLS verification is off, explicitly and in one place.** GRID0+'s certificate is signed by the operator's own CA, which a homebrew can't be told to trust. The
  connection is to the operator's own server on their own network, so verification buys nothing, but it is disabled with a comment saying why. Use `http://` if you
  would rather have no TLS than unverified TLS.
- **The title id is Splatoon 3's.** BCAT save data is per title, so a wrong id mounts a different game's cache, which succeeds and then does nothing visible.

## Building

```sh
sudo dkp-pacman -S switch-dev switch-curl switch-mbedtls
make
```

## Without the installer

The endpoints it uses are ordinary HTTP:

```sh
curl -H "Authorization: Bearer $TOKEN" https://server:8087/api/seed/manifest
curl -H "Authorization: Bearer $TOKEN" -O https://server:8087/api/seed/file/eu-default/base.pack.zs.enc
```

Then put the files into Splatoon 3's BCAT save data with JKSV, keeping the `<region>-default/` layout.
