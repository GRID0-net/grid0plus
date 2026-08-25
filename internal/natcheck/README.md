# internal/natcheck

The NAT-check (NCS) responder. It carries no gameplay traffic, but without it
peer-to-peer play does not start.

| | |
| --- | --- |
| Reimplements | `nncs1-lp1.n.n.srv.nintendo.net` |
| | `nncs2-lp1.n.n.srv.nintendo.net` |
| Reference | [NAT Check Server](https://github.com/kinnay/NintendoClients/wiki/NAT-Check-Server) |
| Protocol | UDP, ports 10025 and 10125 |
| Needs a database | no |

## What it does

The console sends a 16-byte message; the server fills in the source address it
saw and sends it back. From that the console learns its own external address and,
by comparing **which socket answered which message type**, classifies its
router's NAT.

Message layout, big-endian:

```
0x0  4  message type
0x4  4  external port   (the server fills this in)
0x8  4  external IP     (the server fills this in)
0xC  4  the console's external IP outbound; the server's local IP inbound
```

| Type | Must be answered from |
| --- | --- |
| 1 | the regular address and port |
| 2 | **a different address** |
| 3 | the regular address, a different port |
| 4, 5 | the regular address and port (mapping probes) |
| 101, 103 | like 1 |
| 102 | like 3 |

## The two-address requirement

**`nncs1` and `nncs2` must resolve to two different IP addresses.**

Message type 2 has to be answered from a different address, and the console
probes the two hostnames as separate servers. Point both at one IP and the
console de-duplicates the probe, never sends the second one, and NAT detection
does not complete. In the deployment this was checked against, that surfaced as
Mario Kart 8 error 2618-201 and looked nothing like a NAT problem.

A second address on the same machine is enough. `make config-check` warns when
`network.natcheck_secondary_ip` is unset, and the config validator refuses to
accept the two being equal.

Running with one address does work — type 2 falls back to the secondary port —
but consoles then report a stricter NAT type than their router really has, and
some titles refuse to matchmake on that basis.

## Docker

The container uses `network_mode: host`, not a port mapping. Docker's userland
proxy rewrites the source address of the reply, so every message type would
appear to come from the same place and classification would be wrong — in a way
that looks like a NAT problem rather than a Docker one.
