# stunresponder

A minimal STUN (RFC 5389) Binding responder.

| | |
| --- | --- |
| Implements | the Binding usage of RFC 5389 only (request to `XOR-MAPPED-ADDRESS` response) |
| Reference | [RFC 5389](https://datatracker.ietf.org/doc/html/rfc5389) |
| Protocol | UDP, port 3478 by default (`natcheck.stun_listen`) |
| Needs a database | no |

## Why it exists

Splatoon 3 sends STUN Binding requests to whatever host the NPLN latency-measurement list
names, before matchmaking starts. If nothing answers, the console's hall reports "A
communication error has occurred" a few seconds later, and because the request never reaches
the gRPC gateway nothing shows up in the application logs.

STUN is a generic, publicly documented protocol, in the same category as
[`internal/natcheck`](../natcheck/README.md)'s responder.

## What it does

Only `Binding` (message type `0x0001`) is implemented. A request is answered with a Binding
Success Response (`0x0101`) carrying one attribute, `XOR-MAPPED-ADDRESS`: the request's own
observed source address and port, XORed with the magic cookie per RFC 5389 section 15.2.
Anything that isn't a well-formed Binding Request (wrong magic cookie, too short, a source
address this server can't represent) is ignored silently, the same tolerance `natcheck` has for
traffic that isn't its protocol.

## Where it runs

Alongside `natcheck` in `cmd/natcheck`. Both are small, stateless UDP responders, so one
process serves both rather than adding another systemd unit and firewall rule. Set
`natcheck.stun_listen` to change the port; empty disables it.
