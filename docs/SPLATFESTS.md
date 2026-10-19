# Splatfests

A Splatfest on GRID0+ is two things that have to agree:

- the **announcement**: the fest id, the content pack's name, its revision and the
  phase times, which the server tells the game about; and
- the **content**: the packs the console downloads over BCAT (team names, banners,
  idol dialogue), encrypted with keys the server hands out.

If the two disagree, the game reports a communication error that names nothing. The
tooling builds both from one source so they can't drift apart; don't enter the
values by hand in the dashboard and pair them with an unrelated pack.

## Before you start

**The console.** On firmware 22.5.0, install the Toolbox (version 0.5.9 or later),
apply the GRID0+ settings, and reboot. That puts the BCAT signature patch in place
(see [CONSOLE_SETUP.md](CONSOLE_SETUP.md#bcat-on-firmware-2250)). The console also
needs the normal DNS redirects and certificate setup, and the hosts
`bcat-list-lp1.cdn.nintendo.net` and `bcat-data-lp1.cdn.nintendo.net` must reach
GRID0+, not be null-routed.

**The assets.** Prepare game-ready assets in the subdirectories `base`, `bh`, `wa`, `wb`
and, for a third team, `wc`. Each file is either already in the game's format
(`.bfres`, `.byml`, `.sarc.zs`) or a source file that a converter turns into one (see
[the bcat package README](../internal/bcat/README.md)). Keep them outside the
repository.

**The key material.** Encrypting a pack needs a lookup table that comes from the game
and that this repository doesn't contain. Put it on the server at
`/etc/switchnet/bcat-key-material` (owner `root:switchnet`, mode `0640`), or point
`SWITCHNET_BCAT_KEY_MATERIAL` at it. Without it the build stops and says what is
missing.

## 1. Build the fest, held

From `/opt/switchnet`, use a fresh output directory and a start time on a two-hour UTC
boundary:

```sh
sudo install -d -o switchnet -g switchnet /opt/switchnet/data/fest-builds
sudo -u switchnet bin/switchnetctl -config config/config.yaml fest build \
  -assets /path/to/fest-assets \
  -out /opt/switchnet/data/fest-builds/JUEA-00042 \
  -id JUEA-00042 -resource example \
  -start 2026-10-03T00:00:00Z -regions JP,US,EU,AP
```

The id must contain the region letters `JUEA`, as Nintendo's own ids do (`JUEA-00042`).
The game checks the console's region letter against the id and treats a fest without it
as invalid, which shows up as `BcatInvalid`. An id like `NXTD-00019` fails on every
console.

`fest build` writes the packs and a `summary.yml` beside each, generates the keys and the
revision, and registers the fest in the database **held**, meaning not yet announced.
Note the id, resource, revision and phase times it prints. The start time is when play
opens; the announcement phase begins earlier, as the printed timetable shows.

Adding `-enable` announces the fest and stages its packs in one go. Leave it off for now
unless you want it live immediately.

## 2. Stage the files

If `bcat.seed_dir` is set, `fest build -enable` does this step itself, replacing the
served fest in one swap. To stage a held fest by hand, put the title id between the seed
root and the regional folders:

```sh
sudo install -d -o switchnet -g switchnet /opt/switchnet/data/bcat-native/0100c2500fc20000
sudo cp -a /opt/switchnet/data/fest-builds/JUEA-00042/bcat-seed/. \
  /opt/switchnet/data/bcat-native/0100c2500fc20000/
sudo chown -R switchnet:switchnet /opt/switchnet/data/bcat-native/0100c2500fc20000
```

For example, the EU base pack ends up at
`.../0100c2500fc20000/eu-default/base.pack.zs.enc`, with `summary.yml` beside it. The stub
service reads files on each request, so copying them needs no restart. Check
`SWITCHNET_BCAT_SEED_DIR` in the stubs unit if your deployment keeps them elsewhere.

Probe through the TLS edge before you announce anything:

```sh
curl -ksS --resolve bcat-list-lp1.cdn.nintendo.net:443:127.0.0.1 \
  -o /dev/null -w '%{http_code}\n' \
  https://bcat-list-lp1.cdn.nintendo.net/api/nx/v1/list/nx_data_0100c2500fc20000
```

You want **200** and a non-empty body. **304** means the server has no usable seed for
that title, so the console has nothing new to download. If you get 200 but the console
still shows no fest content, read the `switchnet-stubs` log and check the console's BCAT
cache.

## 3. Announce and check

In the dashboard, find the held fest and press **Announce**, but only after the probe
succeeds. Start or restart Splatoon 3 on the patched console and check that the fest news
and team selection appear, then that the hall opens. Watch `switchnet-stubs` for BCAT list
and data requests and `switchnet-npln` for the fest calls. No BCAT request at all points
at redirection, the patch or the console's cached state. A request followed by a
download or parse error points at the content itself.

## Stopping a fest

Press **Hold** or **Delete** in the dashboard. Deleting a fest's row does **not** remove
its files from `bcat-native`. Remove only that title's staged regional directories, after
checking they belong to the fest you are retiring, and keep a backup outside the served
directory.

## If something goes wrong

| Symptom | Usual cause |
| --- | --- |
| The game says communication error as soon as a fest is announced | The announced id, resource or revision don't match the `summary.yml` of the pack the console has. `fest build` makes them match, so the pack came from somewhere else. |
| `BcatInvalid` | The fest id lacks `JUEA` letters, or its resource name differs from the pack's by even one capital letter (the pack's encryption depends on the exact name). |
| The list probe returns 304 | `bcat.seed_dir` is unset, or has no folder for the title id. |
| The console downloads nothing | The BCAT signature patch isn't installed for this firmware, or the `bcat-*` hosts aren't redirected. |
| Fest times look wrong | Start times must fall on two-hour UTC boundaries. |
