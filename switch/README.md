# Pointing a real Switch at SwitchNet

There are two separate problems here, and they have very different answers:

| | Problem | Solved |
| --- | --- | --- |
| **1** | **Redirection** — making Nintendo's hostnames resolve to your server | **off the console, with DNS.** Nothing on the SD card. |
| **2** | **Certificate trust** — making the console accept your certificate for a Nintendo hostname | **on the console.** There is no way around this; the options differ in how robust they are. |

Most guides conflate the two and solve both with Atmosphère — a hosts file plus a
firmware-gated exefs patch. Only the second half actually needs the console.

> Use this on hardware you own, with games you own. SwitchNet ships none of
> Nintendo's code, keys or data, and produces nothing Nintendo's own servers would
> accept.

- [Part 1: redirection, with DNS](#part-1-redirection-with-dns)
- [Part 2: certificate trust](#part-2-certificate-trust)
- [Verify, in order](#verify-in-order)
- [Troubleshooting by symptom](#troubleshooting-by-symptom)
- [Going back to Nintendo](#going-back-to-nintendo)

---

## Part 1: redirection, with DNS

A Switch resolves hostnames through whatever DNS server its network gives it, and
that is true in stock firmware with nothing modified. So redirection needs no
hosts file, no `enable_dns_mitm`, no homebrew, and nothing that a firmware update
or a tool rewriting your SD card can break.

Pick whichever of these suits your network. They are equivalent; all three read
the same host list, so they cannot disagree about which hosts are yours.

### Option A — the router hands out SwitchNet's DNS (best)

Set your router's DHCP to give out SwitchNet's address as the DNS server. **The
console then needs no configuration at all**, and neither does any other console
you add later.

```sh
make up          # brings up the dns service, among others
```

The `dns` service uses host networking and binds port 53. It answers Nintendo's
hostnames with your address and forwards everything else to the upstream
resolvers in `dns.upstream`.

One thing to get right: **`dns.upstream` must not point back at SwitchNet**,
directly or via a router that forwards here. That is a loop, and the symptom is
every non-Nintendo lookup timing out while Nintendo's work fine. `make
config-check` refuses that configuration.

### Option B — the console's own DNS setting

If you would rather not change the router:

**System Settings → Internet → Internet Settings →** your network **→ Change
Settings → DNS Settings → Manual → Primary DNS =** SwitchNet's address.

Leave Secondary DNS blank or set it to SwitchNet's address too. **Do not put a
public resolver there as a fallback** — the console would use it whenever
SwitchNet is briefly slow, and reach real Nintendo instead.

### Option C — a resolver you already run

If you have dnsmasq, Pi-hole, OpenWrt or Unbound doing DNS for your network,
generate configuration for it instead of running another server:

```sh
make gen-dns     # writes deploy/switchnet.dnsmasq.conf and .unbound.conf
```

Both are generated from the same host list. `deploy/switchnet.dnsmasq.conf` also
works in Pi-hole (`/etc/dnsmasq.d/99-switchnet.conf`) and OpenWrt.

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
  talk straight past SwitchNet, intermittently. All three options above handle
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
one entry appended. A file containing only SwitchNet's CA would break every other
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
`ImportServerPki` with SwitchNet's CA on every context that gets created.

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

## Verify, in order

Each step rules out everything before it, which is what makes debugging on
hardware tractable.

**1. SwitchNet answers on its own machine.**

```sh
curl -fsS localhost:8081/keys | head -c 80
```

**2. DNS redirects.** From any machine on the console's network:

```sh
dig @<SWITCHNET_IP> dauth-lp1.ndas.srv.nintendo.net A     +short   # -> <SWITCHNET_IP>
dig @<SWITCHNET_IP> nncs2-lp1.n.n.srv.nintendo.net  A     +short   # -> the SECOND address
dig @<SWITCHNET_IP> dauth-lp1.ndas.srv.nintendo.net AAAA           # -> NOERROR, no answer
dig @<SWITCHNET_IP> atumn.hac.lp1.d4c.nintendo.net  A     +short   # -> a real Nintendo address
dig @<SWITCHNET_IP> example.com                     A     +short   # -> a real address
```

The third and fourth are the ones people skip. An AAAA answer here, or a
SwitchNet address for `atumn`, is a misconfiguration that will bite later.

**3. The console is using that DNS.** Set `log.level: debug` and open anything
online on the console. You should see `dns: redirected` lines with the console's
address. **No lines at all means the console is not using your resolver** — the
DHCP lease has not renewed (toggle the console's Wi-Fi off and on), or the manual
DNS setting did not take.

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

## Troubleshooting by symptom

| Symptom | Almost certainly |
| --- | --- |
| No `dns:` lines at all in the log | The console is not using your resolver. Renew its DHCP lease, or check the manual DNS setting. |
| `dns: redirected` lines, but nothing from dauth or baas | DNS is working and TLS is not. Certificate trust (part 2) — and if you are using the exefs patch, check it matches your firmware. |
| Nothing anywhere, and the console reports a network error | Same as above: cert trust failing produces total silence, because the connection never becomes a request. |
| Everything works, then stops after a system update | An exefs patch that no longer matches the firmware build id. This is the failure the CertStore approach avoids. |
| The eShop or browser stopped working | A `ssl_TrustedCerts.bdf` override that replaced the store instead of extending it. Delete it and rebuild with `--input`. |
| `invalid_device_account` in the log | The console is presenting a device account this server never issued — it was pointed at a different server before, or the database was reset while the console kept its cached credential. |
| `token is invalid` right after a device token was issued | dauth and baas are reading different key directories. They must share `keys.dir`. |
| Every token rejected as expired | The console's clock. System Settings → System → Date and Time → synchronise via the internet. |
| Friends show, but everybody is offline | Check whether `baas: presence reported` appears at all. |
| A friend shows online but never "Playing X" | `appInfo:appId` is not arriving. `log.level: debug` and look at the presence patch. |
| Matchmaking finds a match that never starts | NAT check. `nncs1` and `nncs2` must resolve to two different addresses. |
| A game says it cannot connect | Expected in phase 1; game servers are phase 2. |
| Non-Nintendo sites stop resolving on the console | `dns.upstream` is looping back to SwitchNet, or the upstreams are unreachable. |

---

## Going back to Nintendo

**Redirection**: undo whichever option you used — remove the DHCP DNS override,
or set the console's DNS back to Automatic. That is it, and it takes effect on the
next lease.

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
