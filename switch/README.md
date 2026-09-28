# Pointing a real Switch at GRID0+

## Native BCAT patch for firmware 22.5.0

`switch/atmosphere/exefs_patches/bcat_signature_bypass/` contains an IPS
patch for the operator-supplied `bcat` sysmodule build
`6D9772A733E2370B3F401EFEB7CA8F664309BD66000000000000000000000000`.
It changes the boolean stored after the central RSA verification callback
from the callback's result to true. The patch is local to that build and
does not create or impersonate a Nintendo signature. At module RVA
`0x944dc`, the original instruction is `and w8, w0, #1` (`08 00 00 12`);
the patched instruction is `mov w8, #1` (`28 00 80 52`). The IPS record
offset is `0x945dc`, including Atmosphere's required 0x100 NSO-header
offset. The original bytes were checked against the operator's own 22.5.0
dump, which is not stored here. Firmware updates require a new patch.

Copy the `atmosphere/` subtree to the SD card and reboot before testing
native BCAT delivery. On a console that runs the GRID0+ Toolbox this is
already done for you: the same bytes ship in that repository's `romfs/sd/`
mirror as of v0.5.9, under both build-id filename spellings Atmosphère
accepts, so updating the `.nro` and re-applying ("Switch to SwitchNet") is
the normal route — the copy in this repository is only for a console that
does not run it. The server also needs operator-supplied files under
`bcat.seed_dir`; without them it continues answering 304. See
`internal/bcat/README.md`. This patch has not yet been hardware-verified.

There are two separate problems here, and they have very different answers:

| | Problem | Solved |
| --- | --- | --- |
| **1** | **Redirection** — making Nintendo's hostnames resolve to your server | **off the console, with DNS.** Nothing on the SD card. |
| **2** | **Certificate trust** — making the console accept your certificate for a Nintendo hostname | **on the console.** There is no way around this; the options differ in how robust they are. |
| **3** | **Splatoon 3's own certificate trust** — the game does not use the console's CA store | **on the console, and separately.** Part 2 does not cover it. |

Most guides conflate the two and solve both with Atmosphère — a hosts file plus a
firmware-gated exefs patch. Only the second half actually needs the console.

> **What this deployment actually runs.** The GRID0+ Toolbox homebrew
> ([`n-popescu/grid0plus-toolbox`](https://github.com/n-popescu/grid0plus-toolbox),
> private) on an emuMMC that only ever talks to GRID0+: Atmosphère's hosts
> file for redirection, the public `disable_ca_verification` /
> `disable_browser_ca_verification` patches for trust, and its own Splatoon 3
> patches for part 3. The DNS and CA-store routes below are the alternatives,
> and the reasoning for preferring them where you can.

> Use this on hardware you own, with games you own. GRID0+ ships none of
> Nintendo's code, keys or data, and produces nothing Nintendo's own servers would
> accept.

- [Part 1: redirection, with DNS](#part-1-redirection-with-dns)
- [Part 2: certificate trust](#part-2-certificate-trust)
- [Part 3: Splatoon 3 pins its own certificates](#part-3-splatoon-3-pins-its-own-certificates)
- [Verify, in order](#verify-in-order)
- [Verify Splatoon 3, in order](#verify-splatoon-3-in-order)
- [Troubleshooting by symptom](#troubleshooting-by-symptom)
- [Going back to Nintendo](#going-back-to-nintendo)

---

## Part 1: redirection, with DNS

A Switch resolves hostnames through whatever DNS server its network gives it, and
that is true in stock firmware with nothing modified. So redirection needs no
hosts file, no `enable_dns_mitm`, no homebrew, and nothing that a firmware update
or a tool rewriting your SD card can break.

GRID0+ does not run its own DNS server. Instead it generates configuration for
a resolver you already run (a router doing dnsmasq, Pi-hole, OpenWrt or Unbound):

```sh
make gen-dns     # writes deploy/switchnet.dnsmasq.conf and .unbound.conf
```

Both are generated from the same host list, so they cannot disagree about which
hosts are yours. `deploy/switchnet.dnsmasq.conf` also works in Pi-hole
(`/etc/dnsmasq.d/99-switchnet.conf`) and OpenWrt. Add the one that matches your
resolver, then restart it.

Once that resolver has the configuration, pick whichever of these suits your
network:

### Option A — the router hands it out over DHCP (best)

If the resolver you just configured is (or is reachable from) your router's
DHCP-advertised DNS server, **the console then needs no configuration at all**,
and neither does any other console you add later.

### Option B — the console's own DNS setting

If you would rather not change the router:

**System Settings → Internet → Internet Settings →** your network **→ Change
Settings → DNS Settings → Manual → Primary DNS =** your resolver's address.

Leave Secondary DNS blank or set it to your resolver's address too. **Do not put
a public resolver there as a fallback** — the console would use it whenever your
resolver is briefly slow, and reach real Nintendo instead.

### What the DNS layer does, and one thing to know about it

- Nintendo's account, friends, stub and NAT-check hosts → your address.
- `nncs2` → your **second** address, which must differ from the first. Pia probes
  the two NAT-check hosts as separate servers; with one address the console
  de-duplicates its second probe and NAT detection never completes.
- Telemetry → `0.0.0.0`, so the console gives up immediately.
- The system update hosts and the phase 3 eShop hosts → **forwarded to real
  Nintendo**, deliberately. Redirecting a system update host makes `nim` write a
  persistent flag that survives switching back.
- **AAAA queries for redirected names get NODATA**, not a forwarded answer. This
  matters more than it looks: if AAAA were forwarded while A was answered
  locally, a console on a network with IPv6 would get Nintendo's real address and
  talk straight past GRID0+, intermittently. All three options above handle
  this; a hand-written hosts file in a resolver often does not.

### Does the console use DNS-over-HTTPS?

Not for its system services, as far as anyone has observed — they use the
configured resolver. If you find a service that bypasses it, that is worth
capturing and reporting.

If you want to be certain, block outbound port 853 (DoT) and outbound 443 to
known DoH providers at the router. Belt and braces; not normally needed.

---

## Part 2: certificate trust

**This is the half that has to happen on the console, and there is no way
around it.**

The console asks for `dauth-lp1.ndas.srv.nintendo.net`. That certificate is signed
by *Nintendo CA - G3*, a private CA. No public certificate authority will issue
you a certificate for a Nintendo hostname, and you cannot obtain Nintendo's CA
key. So the console has to be told to accept yours.

### Why not Prelude's approach

Prelude installs `exefs_patches/disable_ca_verification`, an IPS patch against the
`ssl` sysmodule. It works, and it has two properties worth avoiding:

- **It is gated on the firmware build id.** A patch built for one firmware does
  nothing on another — and it fails *silently*. Every HTTPS call fails at the
  handshake and **nothing appears in any log**, because the connection never
  becomes a request. That is the single most confusing failure in this whole
  stack.
- **It switches certificate verification off system-wide.** While it is installed,
  anybody on your network can intercept the console's traffic — including its
  traffic to real Nintendo, with your real account. Prelude's own source calls
  leaving it in place while on Nintendo's servers an obvious ban vector, and
  removes it when switching back.

### The approach to try first: extend the certificate store

The Switch's list of trusted CAs is not compiled into the `ssl` sysmodule. It is a
**data file in a SystemData archive**:

```
title    0100000000000800
romfs    /ssl_TrustedCerts.bdf
```

The `ssl` sysmodule's SSL context uses that store for every connection, and the
file format is documented
([switchbrew](https://switchbrew.org/wiki/SSL_services#CertStore)). Atmosphère's
LayeredFS can replace a file in a data archive's RomFS. So adding an entry to that
file adds a trusted CA.

That is better in every way that matters:

- **Keyed on title id, not firmware build id.** No per-firmware patch, and no
  silent no-op after a system update.
- **It adds trust rather than removing verification.** Everything else still
  validates normally, so the console is not left interceptable when it talks to
  real Nintendo. The ban vector goes away.
- **It is data, not code.** It cannot crash the sysmodule.
- **Reversible by deleting one file.**

There is also no server-side pinning to worry about: `RegisterInternalPki` only
selects the *client* certificate, and the default verify options are `PeerCa` and
`HostName` — so a certificate that chains to a CA in the store and carries the
right hostname in its SANs is accepted. `make gen-certs` already produces exactly
that.

#### First, find out whether Atmosphère will serve the file at all

**This is the part that is not confirmed, and it is worth five minutes before you
spend an hour.**

Atmosphère's `fs.mitm` does not MITM every process. Reading its source
(`ShouldMitmProgramId`), it MITMs applications plus `ns`, `settings` and
`sdb`/`glue` — and **not** arbitrary system modules. So it depends on whether the
`ssl` sysmodule reads the CertStore through `sdb`/`glue`, which is MITM'd
explicitly "to support sd-romfs redirection of common system archives", or
directly through `fs`, which is not.

Custom system fonts work by exactly this mechanism, which is encouraging but not
proof.

The test:

1. Dump the original `ssl_TrustedCerts.bdf` from title `0100000000000800`.
   `nxdumptool` can dump system titles; you want the RomFS file, not the NCA.

2. Check the tool understands your firmware's copy:

   ```sh
   python3 scripts/build-certstore.py --inspect ssl_TrustedCerts.bdf
   ```

   You should see `NintendoCAG3`, `NintendoClass2CAG3` and a few dozen public
   roots, all `EnabledTrusted`.

3. Put a **deliberately broken** copy in the LayeredFS path — truncate it to 8
   bytes:

   ```
   /atmosphere/contents/0100000000000800/romfs/ssl_TrustedCerts.bdf
   ```

4. Reboot and open the eShop or the browser.

   - **TLS breaks** → Atmosphère is serving your file to the `ssl` sysmodule. The
     mechanism works. Go to the next step.
   - **Nothing changes** → it is not being read. Skip to
     [the fallback](#the-fallback-an-ssl-mitm-sysmodule).

5. Either way, **delete the broken file** before continuing.

#### Then install it for real

```sh
make gen-certs        # if you have not already: creates certs/rootCA.pem

python3 scripts/build-certstore.py \
    --input  ssl_TrustedCerts.bdf \
    --ca     certs/rootCA.pem \
    --output out/ssl_TrustedCerts.bdf
```

Copy `out/ssl_TrustedCerts.bdf` to
`/atmosphere/contents/0100000000000800/romfs/ssl_TrustedCerts.bdf` and reboot.

**Never build that file from scratch.** It has to be your console's own store with
one entry appended. A file containing only GRID0+'s CA would break every other
TLS connection on the console: the browser, the eShop, Nintendo Account. The tool
refuses to run without `--input` for this reason, and verifies the round trip
before writing.

Two caveats, stated plainly:

- Whether the certificate blobs are DER or PEM is inferred from your file rather
  than documented. The tool detects it and matches; `--inspect` shows you which.
- A system update replaces the real archive. The LayeredFS file survives, but if
  Nintendo changes the format your override could stop parsing. Re-run
  `--inspect` after a firmware update.

### The fallback: an `ssl` MITM sysmodule

If step 4 showed the override is not being read, the next-best option is a small
Atmosphère MITM sysmodule that intercepts `ssl` and `ssl:s` and calls
`ImportServerPki` with GRID0+'s CA on every context that gets created.

This keeps the two properties that matter — **not gated on a firmware build id**,
and **adds trust rather than disabling verification** — at the cost of being code
rather than a data file. It is not written yet; it is the right thing to build if
the CertStore route does not pan out, and it is tracked in
[`../docs/ROADMAP.md`](../docs/ROADMAP.md).

Note that Prelude shipped something like this (`network_mitm`) and later removed
it because it stopped loading on current firmware. That is a reason to write it
carefully, not a reason the approach is wrong.

### Last resort: the exefs patch

`disable_ca_verification`, as described above. It works today. If you use it,
know what you are accepting, and **remove it before pointing the console back at
Nintendo**.

---

## Part 3: Splatoon 3 pins its own certificates

**Part 2 does not cover this, and finding that out the hard way is expensive.**

Splatoon 3 carries its own statically linked gRPC and TLS stack and pins its own
certificates, independently of the console's CA store. So a console that reaches
BAAS perfectly — friend codes, friends, presence, all of part 1 and part 2
working — will still not reach the game server.

**Memorise the failure signature**, because it looks identical to a dozen
unrelated bugs: the TLS handshake **completes**, and then the game abandons the
call before sending a single HTTP/2 HEADERS frame. The game reports a generic
communication error and **nothing useful appears in the server log** — you see a
connection, and no request.

### The route, and what it costs

**Four checks, not one.** On hardware (2026-09-23, v11.3.0) kinnay's patch
alone was not enough: the game also pins the server's public key, sets its
own `VerifyOption` with its own CA set, and installs a separate TLS verify
callback on the NPLN gRPC channel. The Toolbox ships one IPS patch for each
(`s3certpin_bypass`, `s3verifyoption_bypass`, `s3grpcverify_bypass`,
`s3grpcpeer_bypass`); grid0plus-toolbox's `docs/game-specific-tls-pinning.md` has
the addresses and how each was found. `make patch-splatoon3` below builds only
kinnay's patch, for other builds.

`kinnay/NPLN-Protocols` ships `generate_patch.py`, which builds an IPS patch that
disables TLS certificate verification in the game's main NSO. It was written for
exactly this purpose: "if you want to capture traffic or write your own NPLN
servers."

**The operator has accepted this**, so it is a decision already taken rather than
an open question. Two properties have to be written down where somebody will find
them, because both differ from the position this project took in part 2:

- **It *disables* verification rather than *adding* trust.** Part 2 rejects the
  Prelude-style route partly on this ground. An NSO patch is meaningfully
  narrower — one game's binary, not the system TLS stack — but it is the same
  kind of change, and the console will accept any certificate for the hosts
  Splatoon 3 talks to while it is installed.
- **It is gated on the game version, not the firmware.** A title update silently
  stops it applying. That is the same failure mode as `disable_ca_verification`
  wearing different clothes: everything works, then a game update lands, and the
  symptom is the signature above with nothing to say that a patch stopped
  matching. **If Splatoon 3 stops connecting after an update, check this first.**

The patch goes in `/atmosphere/exefs_patches/`. Regenerate it after every title
update.

### When the game will not connect

Suspect, in order: these patches (a title update silently stops them
applying), then the JSON front end (`internal/nplnrest`), then the gRPC
gateway. As of 2026-09-25 all three work on hardware and the game stops later,
in the hall — see `TODO.md` §4.2.

---

## Verify, in order

Each step rules out everything before it, which is what makes debugging on
hardware tractable.

**1. GRID0+ answers on its own machine.**

```sh
curl -fsS localhost:8081/keys | head -c 80
```

**2. DNS redirects.** From any machine on the console's network, against your
resolver (the router, Pi-hole or Unbound you added the generated configuration
to — not GRID0+, which does not answer DNS itself):

```sh
dig @<RESOLVER_IP> dauth-lp1.ndas.srv.nintendo.net A     +short   # -> <SWITCHNET_IP>
dig @<RESOLVER_IP> nncs2-lp1.n.n.srv.nintendo.net  A     +short   # -> the SECOND address
dig @<RESOLVER_IP> dauth-lp1.ndas.srv.nintendo.net AAAA           # -> NOERROR, no answer
dig @<RESOLVER_IP> atumn.hac.lp1.d4c.nintendo.net  A     +short   # -> 0.0.0.0 (see note below)
dig @<RESOLVER_IP> example.com                     A     +short   # -> a real address
```

The third and fourth are the ones people skip. An AAAA answer here is a
misconfiguration that will bite later.

`atumn` (and `sun`, `aqua` — the other d4c system-update hosts) resolve to
`0.0.0.0` on this deployment rather than a real Nintendo address, which is a
deliberate departure from the general advice for these hosts: normally they
stay passthrough, because a failed system update check makes `nim` store a
persistent flag that survives switching DNS back to real Nintendo. That cost
only matters for a console that moves back and forth. This deployment's
emuMMC never does — it is permanently and exclusively GRID0+'s — so
there's nothing to gain from letting it reach real Nintendo for these, and
null-routing them keeps this emuMMC from ever contacting real Nintendo at
all. See `internal/nintendo/hosts.go` and `TestSystemUpdateHostsAreNullRouted`.

**3. The console is using that resolver.** Check your resolver's own query log
(dnsmasq: `--log-queries`; Pi-hole: the Query Log in its web UI; Unbound:
`verbosity: 2`) for the console's address querying `dauth-lp1.ndas.srv.nintendo.net`
and getting your configured answer back. **Nothing there means the console is not
using your resolver** — the DHCP lease has not renewed (toggle the console's
Wi-Fi off and on), or the manual DNS setting did not take.

**4. TLS answers on 443 with your certificate.**

```sh
openssl s_client -connect <SWITCHNET_IP>:443 \
    -servername dauth-lp1.ndas.srv.nintendo.net </dev/null 2>/dev/null \
  | openssl x509 -noout -issuer -subject
```

Issuer should be `SwitchNet Local CA`.

**5. The console gets through the auth chain.** Boot it and watch the log:

```
service=dauth  msg="device authenticated"   device=… serial=… system_version=…
service=baas   msg="baas request"           path=/1.0.0/application/token status=200
service=baas   msg="baas: registered user"  user=… device_account=…
```

`baas: registered user` is the milestone: DNS, TLS, routing and the whole auth
chain are all correct.

**6. Two consoles, a friend code, a request, an accept.** Both should show the
other online.

**7. Launch a game on one.** The other should show "Playing &lt;game&gt;", and the
log should show `presence reported … state=PLAYING title=…`.

**8. Turn registration off.** Set `baas.allow_registration: false` once every
console is registered.

---

## Verify Splatoon 3, in order

A real console has been through S1–S3 and reaches the hall, where it
currently stops (`TODO.md` §4.2). The checklist is still the order to debug a
new console or a new game version in.

Do part 1 and part 2 first, all the way to step 5 above. Then:

**S1. Set `npln.rest_record_dir` before the first attempt.** The game's first
contact is JSON over HTTPS to `vermillion` and `penne`. With this set, one boot
writes every request and response to disk, hexdumped, in order, credentials
redacted — the fastest way to see where a new console stops.

**S2. Install part 3's patch**, and check `nncs1`/`nncs2` still resolve to two
different addresses.

**S3. Launch the game and watch for the JSON front end.**

```
service=npln listener=rest msg="request" host=gw.hac.lp1.vermillion… path=…
service=npln listener=rest msg="request" host=val.hac.lp1.penne…      path=…
service=npln listener=rest msg="frontline connection open" host=fro-3…
```

`frontline connection open` is the first real milestone: DNS, TLS, part 3's patch
and the routing are all correct, and the console has held a connection open. If
you see the first two lines and never the third, `penne`'s response did not name
the frontline host in a field the game recognises — which is exactly what S1's
transcript settles.

**S4. Watch for the gRPC gateway.**

```
service=npln msg="npln: issued a session" user=…
```

This is the phase 1 → phase 2 seam: BAAS's id token was accepted and an NPLN
session issued. If the front end worked and this never appears, part 3's patch is
the suspect — the handshake completes and the game abandons the call.

**S5. Read the unimplemented-RPC log. This is the to-do list.**

```
service=npln msg="npln: the console asked for an RPC this server does not implement" method=…
```

Every service is registered precisely so these lines exist. What the game asks
for, in the order it asks, is the only reliable guide to what to build next.

**S6. Watch for the matchmaking mode names.**

```
service=npln msg="npln: matchmaking mode has no configured size" mode=…
```

Nothing public documents the strings the game puts in a ticket's
`matchmaking_config`. This line is how they are discovered; put them in
`npln.mode_players`.

**S7. Two consoles, one mode.** Both should be told about the same session with
the same host, and the dashboard's Matches tab should show it. Two consoles are
enough: an even number of players below `npln.players_per_match` (8) starts
after `npln.versus_grace_period` (20 seconds) with nobody else arriving.

**S8. A Splatfest.** `switchnetctl fest build`, install the seed with
`switch/bcat-installer` or by hand, and open the game. If the fest does not
appear, the announced id, resource and revision must match the `summary.yml` just
installed — `fest build` makes them match by construction, so a mismatch means
the seed on the console came from somewhere else.

---

## Troubleshooting by symptom

| Symptom | Almost certainly |
| --- | --- |
| Nothing for the console in your resolver's query log | The console is not using your resolver. Renew its DHCP lease, or check the manual DNS setting. |
| Your resolver's query log shows the console being redirected, but nothing from dauth or baas | DNS is working and TLS is not. Certificate trust (part 2) — and if you are using the exefs patch, check it matches your firmware. |
| Nothing anywhere, and the console reports a network error | Same as above: cert trust failing produces total silence, because the connection never becomes a request. |
| Everything works, then stops after a system update | An exefs patch that no longer matches the firmware build id. This is the failure the CertStore approach avoids. |
| The eShop or browser stopped working | A `ssl_TrustedCerts.bdf` override that replaced the store instead of extending it. Delete it and rebuild with `--input`. |
| `invalid_device_account` in the log | The console is presenting a device account this server never issued — it was pointed at a different server before, or the database was reset while the console kept its cached credential. |
| `token is invalid` right after a device token was issued | dauth and baas are reading different key directories. They must share `keys.dir`. |
| Every token rejected as expired | The console's clock. System Settings → System → Date and Time → synchronise via the internet. |
| Friends show, but everybody is offline | Check whether `baas: presence reported` appears at all. |
| A friend shows online but never "Playing X" | `appInfo:appId` is not arriving. `log.level: debug` and look at the presence patch. |
| Matchmaking finds a match that never starts | NAT check. `nncs1` and `nncs2` must resolve to two different addresses. |
| Splatoon 3 says it cannot connect, and the server log shows a connection but no request | Part 3. The game pins its own certificates and the TLS handshake completing tells you nothing. |
| Splatoon 3 worked and stopped after a **game** update | Part 3's patch is gated on the game version and silently stopped applying. Regenerate it. |
| The JSON front end is reached but the frontline connection never opens | `penne`'s response did not name the frontline host in a field the game recognises. Set `npln.rest_record_dir` and read the transcript. |
| The lobby fills and the match never starts | `npln.ice_servers` — the addresses must be reachable *from the console*, not from the server. |
| A Splatfest is announced and the game reports a communication error | The announced id, resource and revision do not match the seed on the console. `switchnetctl fest build` makes them match by construction. |
| Another game says it cannot connect | Expected: only Splatoon 3 has a game server here. |
| Non-Nintendo sites stop resolving on the console | Your resolver's own upstream is misconfigured or unreachable — check its settings, not GRID0+'s. |

---

## Going back to Nintendo

**This deployment never does.** Its emuMMC is GRID0+'s permanently; nothing
on it talks to Nintendo, which is also why `hosts.go` null-routes the system
update hosts. The steps below are for a console that moves between the two.

**Redirection**: undo whichever option you used — remove the generated
configuration from your resolver and the DHCP DNS override, or set the console's
DNS back to Automatic. That is it, and it takes effect on the next lease.

**Splatoon 3's patch**: delete it from `/atmosphere/exefs_patches/`. Leaving it
installed means the game talks to real Nintendo with certificate verification
off, so anybody on the network can intercept it.

**Certificate trust**: this is the part that matters.

- CertStore override: delete
  `/atmosphere/contents/0100000000000800/romfs/ssl_TrustedCerts.bdf`. You can
  leave it in place with less risk than the alternatives — it only adds a CA whose
  key you hold — but deleting it is one file and restores the console exactly.
- exefs patch: **delete it.** Leaving `disable_ca_verification` installed means
  the console talks to real Nintendo with certificate validation off
  system-wide, so anybody on the network can intercept it.

## Files here

| File | What |
| --- | --- |
| `hosts.txt` | Generated by `make gen-hosts`. Only needed if you cannot control DNS on the network. Gitignored: it contains your addresses. |
| `ca/rootCA.pem` | Generated by `make gen-certs`. Gitignored. |
| `bcat-installer/` | A homebrew that fetches a Splatfest's delivery cache from GRID0+ and writes it into Splatoon 3's BCAT save data. **Not compiled or tested** — read its README before running it. |
