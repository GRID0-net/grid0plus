# natcheck

The NAT-check (NCS) responder. It carries no gameplay traffic, but peer-to-peer play doesn't
start without it.

| | |
| --- | --- |
| Stands in for | `nncs1-lp1.n.n.srv.nintendo.net` and `nncs2-lp1.n.n.srv.nintendo.net` |
| Reference | [NAT Check Server](https://github.com/kinnay/NintendoClients/wiki/NAT-Check-Server) |
| Protocol | UDP, ports 10025 and 10125 |
| Needs a database | no |

## What it does

The console sends a 16-byte message and the server fills in the source address it saw and
sends it back. From that, and from **which socket answered which message type**, the console
learns its external address and classifies its router's NAT. Messages are big-endian:

```
0x0  4  message type
0x4  4  external port   (filled in by the server)
0x8  4  external IP     (filled in by the server)
0xC  4  the console's external IP outbound; the server's local IP inbound
```

| Type | Answered from |
| --- | --- |
| 1 | the regular address and port |
| 2 | **a different address** |
| 3 | the regular address, a different port |
| 4, 5 | the regular address and port (mapping probes) |
| 101, 103 | like 1 |
| 102 | the regular address and port by default (`type102_same_port`); like 3 when off |

Type 102 is answered from the port it was sent to, because a reply from 10125 is dropped by
a port-restricted home router.

## The two-address requirement

`nncs1` and `nncs2` must resolve to two different IP addresses. Message type 2 has to be
answered from a different address, and the console probes the two hostnames as separate
servers. With one address the console de-duplicates the probe, never sends the second one and
NAT detection doesn't complete. It showed up as Mario Kart 8 error 2618-201 and looked
nothing like a NAT problem.

A second address on the same machine is enough. `make config-check` warns when
`network.natcheck_secondary_ip` is unset, and the config validator refuses to accept the two
being equal. Running with one address works, since type 2 falls back to the secondary port,
but consoles then report a stricter NAT than their router really has and some titles refuse to
matchmake.

## STUN

`cmd/natcheck` also runs [`internal/stunresponder`](../stunresponder/README.md), an RFC 5389
Binding responder, on `natcheck.stun_listen` (default `:3478`). It is a different protocol but
the same kind of small stateless UDP responder, so it shares this service.

## Why it runs natively

The protocol works by the console observing which address and port answered. `natcheck` talks
directly on the addresses it is configured with (`primary_listen`, `secondary_listen`,
`alternate_listen`) with nothing in between. Behind Docker's userland proxy, every reply's
source address is rewritten, so every message type seems to come from the same place and the
NAT type comes out wrong in a way that looks like a router problem.

## Sharing what it observes

With `redis.url` set, the source address of every reachability message (the external port the
console's router mapped for its peer-to-peer socket) is written to Redis through
[`internal/natobs`](../natobs/README.md), off the read loop so a slow Redis never delays a
reply. `cmd/nex` uses it to correct the public stations it hands out for Splatoon 2.
