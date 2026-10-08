# Activate a Splatoon 3 Splatfest with server-delivered BCAT

This is the operator procedure for publishing a fest on GRID0+. The server's
fest announcement and BCAT packs must refer to the **same** fest ID, resource,
revision, and keys. Build them together; do not create a dashboard fest with
hand-entered values and pair it with an unrelated pack.

**Current limit:** GRID0+'s native BCAT list/data responses and the firmware
22.5.0 signature patch exist, but automatic download and acceptance on real
hardware have not yet been confirmed. A successful HTTP probe below proves the
server can supply a list; it does not prove that the Switch installed the
files. The [BCAT cache installer](../switch/bcat-installer/README.md) is the
fallback if native download fails.

## 1. Prepare the Switch

On firmware **22.5.0**, install the latest GRID0+ Toolbox, run its **Switch to
SwitchNet / Apply** action, and reboot. The Toolbox release must include the
`bcat_signature_bypass` patch (v0.5.9 or later). The patch is specific to the
`bcat` sysmodule build described in [switch/README.md](../switch/README.md);
another firmware needs a patch for its own build. The console also needs the
normal GRID0+ DNS redirects and certificate setup. In particular, the
`bcat-list-lp1.cdn.nintendo.net` and `bcat-data-lp1.cdn.nintendo.net` hosts
must reach GRID0+ rather than be blackholed.

## 2. Build the fest, initially held

Prepare game-ready assets in the `base`, `bh`, `wa`, `wb`, and (for a third
team) `wc` subdirectories. See [internal/bcat/README.md](../internal/bcat/README.md)
for required formats. Assets and the IV-derivation material are operator-local;
do not commit either. Set `SWITCHNET_BCAT_KEY_MATERIAL` to the path of that
local material before running the command, or install it once at
`/etc/switchnet/bcat-key-material` (`root:switchnet`, mode `0640`); the build
reads that file when the variable is unset. Either way it stays on the server
and out of this repository.

From `/opt/switchnet` on the VM, use a new output directory and a two-hour
aligned UTC start time. Substitute your own ID, resource, date, and asset path:

```sh
cd /opt/switchnet
sudo install -d -o switchnet -g switchnet /opt/switchnet/data/fest-builds
sudo -u switchnet bin/switchnetctl -config config/config.yaml fest build \
  -assets /path/to/game-ready/fest-assets \
  -out /opt/switchnet/data/fest-builds/JUEA-00042 \
  -id JUEA-00042 -resource example \
  -start 2026-10-03T00:00:00Z -regions JP,US,EU,AP
```

**The fest ID must contain the region letters `JUEA`** (Nintendo's own IDs
are `JUEA-xxxxx`). The game checks the ID for the letter of the console's
region (J, U, E or A; `FUN_7103531410` in v11.3.0) and treats a fest without
it as invalid for that console, which shows as BcatInvalid. An ID such as
`NXTD-00019` fails on every console.

**Do not add `-enable` yet** unless you want it live now: `-enable` both
announces the fest and stages its packs (step 3) in one go. `fest build` writes the packs and their
`summary.yml` files, generates matching keys and revision, and registers a
held fest in the database. Keep the command's printed ID, resource, revision,
and phase times. The start is when play opens; the announcement phase begins
before that according to the printed timetable. If an asset or key-material
check fails, fix that before proceeding. The `switchnet` user must be able to
read the assets and key-material file; keep that file outside the repository.

## 3. Stage the files on the native BCAT server

`fest build -enable` does this step itself when `bcat.seed_dir` is set,
replacing the served fest in one swap. The manual procedure below is for a
fest built held and announced later.

The current VM's `switchnet-stubs` unit sets `SWITCHNET_BCAT_SEED_DIR` to
`/opt/switchnet/data/bcat-native`. The native reader expects the Splatoon 3
title ID *between* that root and the regional directories. For the example
build above:

```sh
sudo install -d -o switchnet -g switchnet \
  /opt/switchnet/data/bcat-native/0100c2500fc20000
sudo cp -a /opt/switchnet/data/fest-builds/JUEA-00042/bcat-seed/. \
  /opt/switchnet/data/bcat-native/0100c2500fc20000/
sudo chown -R switchnet:switchnet \
  /opt/switchnet/data/bcat-native/0100c2500fc20000
```

For example, the EU pack must end up at
`/opt/switchnet/data/bcat-native/0100c2500fc20000/eu-default/base.pack.zs.enc`,
with `summary.yml` beside it. The stubs service reads files on each request;
copying them does not require a rebuild or restart. If this deployment moves,
check `SWITCHNET_BCAT_SEED_DIR` in the stubs unit rather than assuming this path.

Probe the live server through its TLS edge **before announcing**:

```sh
curl -ksS --resolve bcat-list-lp1.cdn.nintendo.net:443:127.0.0.1 \
  -o /dev/null -w '%{http_code}\n' \
  https://bcat-list-lp1.cdn.nintendo.net/api/nx/v1/list/nx_data_0100c2500fc20000
```

Expect **200** and a nonempty response for a staged title. **304** means the
server has no usable native seed and the console has nothing new to download.
If the server returns 200 but the console still has no fest content, inspect
`switchnet-stubs` logs and the console's BCAT delivery cache; do not assume
that a successful server-side probe proves installation.

## 4. Announce, then check the console

In the GRID0+ dashboard, find the held fest and press **Announce**. Only do
this after the BCAT list probe succeeds. Start or restart Splatoon 3 on the
patched Switch. Check whether the fest news and team-selection screen appear,
then whether the hall opens. Watch `switchnet-stubs` logs for BCAT list and
data requests and `switchnet-npln` logs for the fest RPCs. A missing BCAT
request points toward redirection, patch, or cache-refresh behavior; a request
followed by a download/parse error points toward the native delivery format.

To stop a fest, use **Hold** or **Delete** on the dashboard. Deleting its
database row does **not** remove files under `bcat-native`; remove only that
title's staged regional directories after verifying they belong to the fest
being retired. Preserve any captures and operator backup outside the served
directory.
