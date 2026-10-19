# Pointing a console at GRID0+

Use this on hardware you own, with games you own. GRID0+ ships none of Nintendo's
code, keys or data and produces nothing that Nintendo's servers would accept.

Getting a console to use GRID0+ is three separate problems:

| | Problem | Where it is solved |
| --- | --- | --- |
| 1 | **Redirection**: making Nintendo's hostnames resolve to your server | off the console, with DNS |
| 2 | **Certificate trust**: making the console accept your certificate for a Nintendo hostname | on the console |
| 3 | **Splatoon 3's own certificate checks**: the game ignores the console's trust store | on the console, separately from 2 |

Most guides solve 1 and 2 together with Atmosphère (a hosts file plus a firmware
patch). Only the second half actually needs the console.

**The easy route.** The GRID0+ Toolbox homebrew app (the `toolbox/` folder of
[GRID0-net/GRID0plus-cfw](https://github.com/GRID0-net/GRID0plus-cfw)) does all of
it from one menu: it writes Atmosphère's hosts file from this repository's host list,
installs the certificate patches and Splatoon 3's patches, and reboots. Choosing the
default mode restores the previous hosts folder. If you use it, you can skip to
[Verify, in order](#verify-in-order). The rest of this page explains what it does and
the alternatives.

- [Part 1: redirection with DNS](#part-1-redirection-with-dns)
- [Part 2: certificate trust](#part-2-certificate-trust)
- [Part 3: Splatoon 3](#part-3-splatoon-3)
- [BCAT on firmware 22.5.0](#bcat-on-firmware-2250)
- [Verify, in order](#verify-in-order)
- [Verify Splatoon 3](#verify-splatoon-3)
- [Going back to Nintendo](#going-back-to-nintendo)

## Part 1: redirection with DNS

A Switch resolves hostnames through whatever DNS server its network gives it, even
on stock firmware. So redirection needs no hosts file, no homebrew and nothing a
firmware update can break.

GRID0+ doesn't run a DNS server. It generates configuration for a resolver you
already run (a router with dnsmasq, Pi-hole, OpenWrt or Unbound):

```sh
make gen-dns     # writes deploy/switchnet.dnsmasq.conf and deploy/switchnet.unbound.conf
```

Both are generated from the same host list, so they can't disagree about which hosts
are yours. The dnsmasq file also works in Pi-hole (`/etc/dnsmasq.d/99-switchnet.conf`)
and OpenWrt. Add the one that matches your resolver and restart it. Then pick one of:

**A. The router hands the resolver out over DHCP (best).** The console needs no
configuration at all, and neither does any console you add later.

**B. The console's own DNS setting.** System Settings, Internet, Internet Settings,
your network, Change Settings, DNS Settings, Manual, then set Primary DNS to your
resolver's address. Leave Secondary blank or set it to the same address. Don't put a
public resolver there as a fallback: the console would use it whenever yours is
briefly slow, and reach real Nintendo instead.

If you can't control DNS on the network at all, `make gen-hosts` writes
`switch/hosts.txt` for Atmosphère's hosts folder (`hosts/sysmmc.txt` and
`hosts/emummc.txt`, with `enable_dns_mitm` on).

What the generated DNS configuration does:

- Account, friends, stub and NAT-check hosts go to your address.
- `nncs2` goes to your **second** address, which must differ from the first.
- Telemetry goes to `0.0.0.0`, so the console gives up straight away.
- The system update hosts (`sun`, `atumn`, `aqua`) go to `0.0.0.0` too. If you reuse
  this host list for a console that also talks to real Nintendo, make them pass
  through again first, because a failed update check makes the system store a flag
  that survives switching back.
- `AAAA` queries for redirected names get "no data", never a forwarded answer. If AAAA
  were forwarded while A was answered locally, a console on an IPv6 network would
  sometimes reach the real address and bypass GRID0+.

## Part 2: certificate trust

The console asks for `dauth-lp1.ndas.srv.nintendo.net`. Its certificate is signed by
a private Nintendo CA, no public authority will issue you a certificate for a
Nintendo hostname, and you can't get Nintendo's CA key. So the console has to be
told to accept yours.

There are two ways to do that.

### The firmware patch (what the Toolbox installs)

`disable_ca_verification` and `disable_browser_ca_verification` are public IPS
patches for the `ssl` system module. They work, but you should know what you are
accepting:

- **They are tied to a firmware build id.** Built for one firmware, they do nothing on
  another, and they fail silently: every HTTPS call fails at the handshake and
  nothing appears in any log, because the connection never becomes a request. After a
  firmware update, this is the first thing to check.
- **They switch certificate checking off for the whole system.** While installed,
  anyone on your network can intercept the console's traffic, including traffic to
  real Nintendo. Use them only on a console that stays on GRID0+.

### Extending the console's certificate store

The Switch's list of trusted CAs is a data file, not code: `ssl_TrustedCerts.bdf` in
the system archive for title `0100000000000800`. Atmosphère's LayeredFS can replace
files in such archives, so adding an entry to that file adds a trusted CA. This is the
narrower option:

- it is keyed on a title id, not a firmware build id, so a firmware update doesn't
  silently break it;
- it adds trust instead of removing verification, so the console stays safe against
  interception when it talks to anything else;
- it is data, so it can't crash the module, and deleting one file undoes it.

It depends on Atmosphère's file system module serving that archive to the `ssl`
module. This is worth testing first:

1. Dump the original `ssl_TrustedCerts.bdf` from title `0100000000000800` (nxdumptool
   can dump system titles; you want the RomFS file, not the NCA).
2. Check the tool understands your firmware's copy:
   ```sh
   python3 scripts/build-certstore.py --inspect ssl_TrustedCerts.bdf
   ```
   You should see the Nintendo CAs and a few dozen public roots, all trusted.
3. Put a deliberately broken copy (truncated to 8 bytes) at
   `/atmosphere/contents/0100000000000800/romfs/ssl_TrustedCerts.bdf`, reboot, and open
   the eShop or the browser. If TLS breaks, the override is being read. If nothing
   changes, it isn't, and you need the firmware patch above. Delete the broken file
   either way.

Then build the real one:

```sh
make gen-certs        # if you haven't already: creates certs/rootCA.pem

python3 scripts/build-certstore.py \
    --input  ssl_TrustedCerts.bdf \
    --ca     certs/rootCA.pem \
    --output out/ssl_TrustedCerts.bdf
```

Copy `out/ssl_TrustedCerts.bdf` to
`/atmosphere/contents/0100000000000800/romfs/ssl_TrustedCerts.bdf` and reboot.

**Never build that file from scratch.** It has to be your console's own store with one
entry added. A file holding only GRID0+'s CA would break every other TLS connection on
the console, including the browser and Nintendo Account. The tool refuses to run
without `--input` for this reason, and checks the round trip before writing. A system
update replaces the real archive, so run `--inspect` again after one.

## Part 3: Splatoon 3

Part 2 doesn't cover Splatoon 3. It carries its own TLS and gRPC stack and pins its
own certificates, independently of the console's trust store. A console can reach
BAAS perfectly (friend codes, friends, presence) and still never reach the game
server.

The failure looks the same as a dozen unrelated problems: the TLS handshake
completes, then the game abandons the call before sending a single HTTP/2 request, and
shows a generic communication error. The server log shows a connection and no request.

The Toolbox ships IPS patches for Splatoon 3 version 11.3.0, one per check the game
makes (certificate pinning, peer name, verify options and gRPC verification), plus one
that lets a match start with fewer than eight players. For another game build,
`make patch-splatoon3` builds the upstream patch from your own copy of the game's
`main`:

```sh
make patch-splatoon3 NSO=/path/to/main            # into Ryujinx's mods directory
make patch-splatoon3 NSO=/path/to/main SD=/mnt/sd # onto an Atmosphère SD card
```

Two things to know:

- The patches turn certificate checking off for that one game, rather than adding
  trust. While one is installed, the game will accept any certificate for the hosts it
  talks to.
- They are tied to the game version. **A game update silently stops them applying**,
  and the symptom is the one above. If Splatoon 3 stops connecting after an update,
  check this first.

Patches go in `/atmosphere/exefs_patches/`. Remove them before pointing the console at
real Nintendo.

## BCAT on firmware 22.5.0

BCAT is how consoles download content such as Splatfest packs. The console checks a
signature on what it downloads, so GRID0+ ships an IPS patch that skips the check:
`switch/atmosphere/exefs_patches/bcat_signature_bypass/`. It doesn't create or
impersonate a Nintendo signature; it only stops the system from rejecting content
GRID0+ built.

The patch matches one build of the `bcat` system module, so **a firmware update needs
a new one**. Copy the `atmosphere/` folder to the SD card and reboot. Consoles using
the Toolbox already get it from there (version 0.5.9 or later). The server also needs
files in `bcat.seed_dir`; without them it answers "nothing new". See
[SPLATFESTS.md](SPLATFESTS.md).

## Verify, in order

Each step rules out everything before it.

1. **GRID0+ answers on its own machine.** `curl -fsS localhost:8081/keys | head -c 80`
2. **DNS redirects.** From a machine on the console's network, against your resolver:
   ```sh
   dig @<RESOLVER_IP> dauth-lp1.ndas.srv.nintendo.net A    +short   # your address
   dig @<RESOLVER_IP> nncs2-lp1.n.n.srv.nintendo.net   A    +short   # the second address
   dig @<RESOLVER_IP> dauth-lp1.ndas.srv.nintendo.net AAAA          # no answer
   dig @<RESOLVER_IP> atumn.hac.lp1.d4c.nintendo.net   A    +short   # 0.0.0.0
   dig @<RESOLVER_IP> example.com                      A    +short   # a real address
   ```
3. **The console uses that resolver.** Check your resolver's query log (dnsmasq
   `--log-queries`, Pi-hole's Query Log, Unbound `verbosity: 2`) for the console
   asking for `dauth-lp1.ndas.srv.nintendo.net`. Nothing there means the console isn't
   using your resolver: renew its DHCP lease by toggling Wi-Fi, or check the manual DNS
   setting.
4. **TLS answers on 443 with your certificate.**
   ```sh
   openssl s_client -connect <YOUR_IP>:443 -servername dauth-lp1.ndas.srv.nintendo.net \
       </dev/null 2>/dev/null | openssl x509 -noout -issuer -subject
   ```
5. **The console gets through the authentication chain.** Boot it and watch the log for
   `device authenticated`, a `/1.0.0/application/token` request with status 200, and
   `baas: registered user`. That last line means DNS, TLS, routing and the whole chain
   are right.
6. **Two consoles, a friend code, a request, an accept.** Each shows the other online.
7. **Launch a game on one.** The other shows "Playing <game>".
8. **Turn registration off** with `baas.allow_registration: false` once everyone is in.

## Verify Splatoon 3

Do the steps above first, up to the authentication chain. Then:

1. **Set `npln.rest_record_dir` before the first attempt.** The game's first contact is
   JSON over HTTPS to the `vermillion` and `penne` hosts. With this set, one boot
   writes every request and response to disk, which is the quickest way to see where a
   new console stops.
2. **Install the Splatoon 3 patches** and check `nncs1` and `nncs2` still resolve to two
   different addresses.
3. **Launch the game and watch for the JSON front end:**
   ```
   service=npln listener=rest msg="request" host=gw.hac.lp1.vermillion... path=...
   service=npln listener=rest msg="request" host=val.hac.lp1.penne...      path=...
   service=npln listener=rest msg="frontline connection open" host=fro-3...
   ```
   `frontline connection open` is the first real milestone: DNS, TLS, the patches and
   routing are all right. If you see the first two lines and never the third, check the
   transcript from step 1.
4. **Watch for the gRPC gateway:** `npln: issued a session`. That means BAAS's id token
   was accepted. If the JSON front end worked and this never appears, suspect the
   patches: the handshake completes and the game abandons the call.
5. **Read the unimplemented-RPC log.** Lines like `the console asked for an RPC this
   server does not implement` name what the game asked for and the server can't answer.
6. **Watch for matchmaking mode names.** A line like `matchmaking mode has no configured
   size` names a mode that needs an entry in `npln.mode_players`.
7. **Two consoles, one mode.** Both should be told about the same session with the same
   host, and the dashboard's Matches tab should show it. An even number of players below
   `npln.players_per_match` starts after `npln.versus_grace_period`.
8. **A Splatfest.** See [SPLATFESTS.md](SPLATFESTS.md).

If something doesn't work, [TROUBLESHOOTING.md](TROUBLESHOOTING.md) lists symptoms and
their usual causes.

## Going back to Nintendo

A console that moves between GRID0+ and real Nintendo needs three things undone.

- **Redirection:** remove the generated configuration from your resolver and any DHCP
  DNS override, or set the console's DNS back to Automatic. It takes effect at the next
  lease.
- **Splatoon 3's patches:** delete them from `/atmosphere/exefs_patches/`. Leaving one in
  place means the game talks to real Nintendo with certificate checking off.
- **Certificate trust:** delete `ssl_TrustedCerts.bdf` from
  `/atmosphere/contents/0100000000000800/romfs/` if you used the certificate store
  route. If you used the firmware patch, delete it, since it leaves system-wide
  certificate checking off.

## Files in `switch/`

| File | What it is |
| --- | --- |
| `hosts.txt` | Generated by `make gen-hosts`. Only needed if you can't control DNS. Git-ignored, since it holds your addresses. |
| `atmosphere/exefs_patches/bcat_signature_bypass/` | The BCAT patch described above. |
| `bcat-installer/` | A homebrew app that fetches a Splatfest's delivery cache from GRID0+ and writes it into Splatoon 3's BCAT save data. A fallback for consoles that don't have the BCAT patch. See its README. |
