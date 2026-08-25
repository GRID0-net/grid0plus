# Capturing traffic from a real Switch

How to see what the console actually sends, so a guess in the implementation can
be replaced with a fact. What to do with the result is in
[`CAPTURES/README.md`](CAPTURES/README.md).

- [Start here: capture from a PC, not the console](#start-here-capture-from-a-pc-not-the-console)
- [The ban risk in the console-MITM approach](#the-ban-risk-in-the-console-mitm-approach)
- [The idea behind console MITM](#the-idea-behind-console-mitm)
- [HTTPS, with mitmproxy](#https-with-mitmproxy)
- [Capturing against your own server](#capturing-against-your-own-server)
- [UDP, for NAT check](#udp-for-nat-check)
- [gRPC, for NPLN](#grpc-for-npln)
- [What to capture, in order](#what-to-capture-in-order)

---

## Start here: capture from a PC, not the console

**This is the recommended method.** It gets the real responses without the
console being involved at all.

`kinnay/NintendoClients` can speak the whole Switch auth chain from a computer,
given your console's credentials. So you can call Nintendo's real BAAS friends
API directly and read the JSON, with no proxy, no DNS-MITM, and — crucially —
**without the console ever running with certificate verification disabled while
talking to Nintendo.**

`scripts/capture-baas.py` does it in one command.

### What you need

| | Where from |
| --- | --- |
| `prod.keys` | Lockpick_RCM |
| `PRODINFO` | hekate, decrypted if your setup encrypts it |
| BAAS device account id and password | save folder `8000000000000010`, `su/baas/<guid>.dat` |

```sh
pip install NintendoClients anyio
# if netifaces fails to build: apt install build-essential python3-dev

# read the credentials out of the save file rather than by hand
python3 scripts/capture-baas.py --parse-save /path/to/su/baas/xxxxx.dat
```

### Capture

```sh
python3 scripts/capture-baas.py     --keys /path/to/prod.keys     --prodinfo /path/to/PRODINFO     --device-account-id 0123456789abcdef     --password "<40 characters>"     --country NL     --system-version 2250     --out docs/CAPTURES/raw
```

It writes one JSON file per endpoint and prints a one-line shape summary for
each. That summary is the actual deliverable for several open questions — whether
the friend list is `array[N] of object with keys [...]` or
`object with keys ['friends']` settles the highest-priority guess in the whole
repository.

**Add a friend on the console first.** A friend list with nobody in it, and a
relationship endpoint with no relationship, tell you very little.

### What it sends, and what it does not

It requests a device token, logs in, and performs the friends **reads**. That is
the same traffic the console makes when you open the friends applet. No request
in the script creates, accepts or deletes anything.

It does mean Nintendo sees your real console identity — exactly as it does
whenever the console is online normally. Nothing about this is unusual traffic.

### Request shapes without touching Nintendo at all

```sh
python3 scripts/capture-baas.py ... --against-switchnet https://192.168.1.50
```

Same code, pointed at your own server. You get the **request** shapes — which are
the harder half to guess — with zero Nintendo traffic.

---

## The ban risk in the console-MITM approach

Worth stating plainly before the mitmproxy sections, because it is easy to walk
into.

To MITM the console you have to install the `disable_ca_verification` patch,
which turns off certificate validation **system-wide**. If the console then talks
to real Nintendo with that patch active:

- anybody on your network can intercept its traffic, including your account
  credentials;
- the console produces error 2137-7403 in some flows, which is a fairly obvious
  signal;
- Prelude's own source calls leaving that patch in place while on Nintendo's
  servers "an obvious ban vector", and removes it when switching back.

The PC method above avoids all of this: the console is never patched, and never
online with a modified SSL stack. Prefer it. Use console MITM only for what it
alone can capture — chiefly the console's own **request** bodies for endpoints
that `NintendoClients` does not implement, and NPLN in phase 2.

If you do use it: capture one host at a time, and remove the patch immediately
afterwards.

---

## The idea behind console MITM

The console resolves Nintendo hostnames through Atmosphère's DNS-MITM, so you
already control where its traffic goes. Capturing is a matter of pointing it at
something that records and forwards, rather than at SwitchNet directly.

The certificate does not have to be trusted, because the
`disable_ca_verification` patch is already in place — see
[`../switch/README.md`](../switch/README.md). That is what makes this
straightforward compared to a stock console, where it is not possible at all.

## HTTPS, with mitmproxy

On your PC:

```sh
pip install mitmproxy      # or: brew install mitmproxy
```

Run it in reverse-proxy mode, so it accepts connections for a Nintendo hostname
and forwards to real Nintendo:

```sh
mitmweb --mode reverse:https://dauth-lp1.ndas.srv.nintendo.net \
        --listen-port 443 \
        --set stream_large_bodies=1m
```

Point the console's hosts file at your PC for that host only. Take
`switch/hosts.txt` and change one line:

```
192.168.1.99   dauth-lp1.ndas.srv.nintendo.net     # 192.168.1.99 = your PC
192.168.1.50   e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com
...
```

Redirect one host at a time. Everything through mitmproxy at once produces a log
that is hard to read and a console that is slow enough to time out.

Reboot the console, do the thing you want to capture, and read the flows in
mitmweb. `File → Save` writes a `.flow` file; `mitmproxy -nr flows -w -` prints
them as text.

### If you want the real responses

The above forwards to real Nintendo, so you see genuine responses — which is the
point, since that is what the implementation is being compared against. Two
consequences:

- **Your console talks to Nintendo with its real identity.** That is what a stock
  console does anyway, but be aware that it is not anonymous.
- **You need a genuine account and, for some endpoints, a membership** for the
  request to succeed. A capture of an error response is still useful — it tells
  you the error shape — but it does not tell you the success shape.

### If real Nintendo is not an option

Point mitmproxy at SwitchNet instead:

```sh
mitmweb --mode reverse:http://192.168.1.50:8083 --listen-port 443
```

You then see what the console *sends*, which is half the answer and the half that
is harder to guess. The request shape for `POST /2.0.0/friend_requests` is
capturable this way without touching Nintendo at all, and it is the most valuable
open question in the list.

## Capturing against your own server

For request shapes, you often do not need a proxy at all. Turn the log up:

```yaml
log:
  level: debug
stubs:
  log_bodies: true
```

The BAAS server logs every request with its path and user agent, and warns with
the full path on any patch operation it does not recognise:

```
level=WARN msg="baas: unsupported user patch path" user=… op=replace path=/somethingNew
level=WARN msg="baas: unhandled request" method=POST path=/2.0.0/something
```

Those two lines are the to-do list, and they cost nothing to collect. The user
agent tells you which sysmodule is calling — `nnAccount` or `nnFriends` — which
matters when a capture and the implementation disagree about a request shape.

## UDP, for NAT check

mitmproxy does not do UDP. Use tcpdump on the server:

```sh
sudo tcpdump -i any -n -X 'udp port 10025 or udp port 10125'
```

The messages are 16 bytes, big-endian: message type, external port, external IP,
local IP. `internal/natcheck/server.go` documents the layout and what each type
means.

What to look for: the console should send types 1 through 5, and it decides its
NAT type from *which address and port* each reply came from. If you only ever see
type 1, the console gave up — usually because `nncs1` and `nncs2` resolve to the
same address, so it de-duplicated the second probe.

## gRPC, for NPLN

Phase 2. NPLN is gRPC over HTTP/2, so a plain HTTP proxy will not read it.

- [`grpc-dump`](https://github.com/bradleyjkemp/grpc-tools) — a proxy that dumps
  decoded protobuf messages
- [`proxide`](https://github.com/Rantanen/proxide) — another gRPC debugging proxy
- [`grpcui`](https://github.com/fullstorydev/grpcui) — a browser UI for making
  calls against your own server

The protobuf definitions are published at
[kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols), which also
carries an IPS patch generator that disables TLS verification inside Splatoon 3
specifically — useful because the game does its own verification on top of the
system's.

## What to capture, in order

Each round is useful on its own, so there is no need to get through all of them
before the results are worth having.

**Round 1 — the auth chain.** dauth `device_auth_token`, aauth
`application_auth_token`, BAAS `application/token`, `users`, `login`. This is the
best-documented part, so a capture here is mostly confirmation — but confirming it
tells you your capture setup works before you rely on it for the parts that are
guessed.

**Round 2 — friends.** The valuable one, because none of it is documented. Most
of it is covered by `scripts/capture-baas.py` with no console MITM; only the
console's own request bodies for steps 2, 4, 7, 9 and 10 need a proxy.

1. generate a friend code
2. send a friend request from console A
3. read the inbox on console B
4. accept
5. view the friend list on both
6. launch a game on one and view the friend list on the other
7. mark a friend as a favourite
8. unfriend
9. block, then unblock
10. upload a profile picture

Steps 5 and 6 answer the single most important open question: the friend list
response shape.

**Round 3 — Splatoon 3 NPLN.** Phase 2. Game boot, the NPLN auth handshake,
entering the lobby, a matchmaking request, match found, the P2P handshake,
post-match results, a Salmon Run session, the schedule, the locker.

Write each one up per the template in
[`CAPTURES/README.md`](CAPTURES/README.md), redact the credentials, and delete the
matching `TODO(capture-needed)` comment in the same commit.
