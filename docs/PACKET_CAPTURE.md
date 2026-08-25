# Capturing traffic from a real Switch

How to see what the console actually sends, so a guess in the implementation can
be replaced with a fact. What to do with the result is in
[`CAPTURES/README.md`](CAPTURES/README.md).

- [The idea](#the-idea)
- [HTTPS, with mitmproxy](#https-with-mitmproxy)
- [Capturing against your own server](#capturing-against-your-own-server)
- [UDP, for NAT check](#udp-for-nat-check)
- [gRPC, for NPLN](#grpc-for-npln)
- [What to capture, in order](#what-to-capture-in-order)

---

## The idea

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

**Round 2 — friends.** The valuable one, because none of it is documented:

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
