# internal/stunresponder

A minimal STUN (RFC 5389) Binding responder.

| | |
| --- | --- |
| Reimplements | RFC 5389's Binding usage only (request → XOR-MAPPED-ADDRESS response) |
| Reference | [RFC 5389](https://datatracker.ietf.org/doc/html/rfc5389) |
| Protocol | UDP, port 3478 by default (`natcheck.stun_listen`) |
| Needs a database | no |

## Why this exists

Splatoon 3 sends STUN Binding requests to whatever host
`npln.ListLatencyMeasurementServers` names, before matchmaking starts —
confirmed by a 2026-09-23 packet capture of a real console: every one of its
UDP packets to that host has RFC 5389's magic cookie (`2112a442`) at byte
offset 4, inside an otherwise plain 20-byte Binding Request. Nothing answered
them (the console got `ICMP port unreachable` every time), and a few seconds
later the console's own hall UI reported "A communication error has
occurred" and reloaded — the request never reaches this project's own gRPC
gateway at all, so nothing about that failure was visible in application
logs.

STUN itself is not a Nintendo internal — it is a generic, publicly documented
protocol, in the same category as `internal/natcheck`'s NCS responder. This
package answers it because Splatoon 3 happens to speak it, not because
anything about the wire format needed reverse engineering.

## What it does

Only `Binding` (message type `0x0001`) is implemented — the one message
type the capture shows the console sending. A request is answered with a
Binding Success Response (`0x0101`) carrying one attribute,
`XOR-MAPPED-ADDRESS`: the request's own observed source address and port,
XORed with the magic cookie per RFC 5389 §15.2. Anything that is not a
well-formed Binding Request (wrong magic cookie, too short, a source address
this server cannot represent) is silently ignored, the same tolerance
`internal/natcheck` has for traffic that is not its protocol.

## Where it runs

Alongside `internal/natcheck`, in `cmd/natcheck` — both are small,
protocol-specific UDP responders with no state and no database, so one
process serves both rather than adding a second systemd unit and a second
NSG rule for what is, operationally, the same kind of thing. Set
`natcheck.stun_listen` (default `:3478`) to change the port; leaving it empty
disables the responder.
