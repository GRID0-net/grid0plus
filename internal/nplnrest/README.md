# internal/nplnrest

NPLN's JSON front end: the hosts Splatoon 3 talks to **before** it opens a gRPC
channel. This is earlier in the boot sequence than `internal/npln`, and a
console that cannot get past it never reaches the gateway at all.

| | |
| --- | --- |
| Reimplements | `gw.hac.lp1.vermillion.srv.nintendo.net`, `god.hac.lp1.penne.srv.nintendo.net`, `val.hac.lp1.penne.srv.nintendo.net`, `fro-N.hac.lp1.penne.srv.nintendo.net` |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers), [`docs/CAPTURES/npns-sysmodule-decompile.md`](../../docs/CAPTURES/npns-sysmodule-decompile.md) |
| Needs a database | for push notifications only (penne tokens, push channels, tickets) |

| Host | What it does |
| --- | --- |
| `gw.hac.lp1.vermillion…` | device initialisation, account config |
| `god.hac.lp1.penne…` | npns's penne id and notification tokens, remembered so a push can find the console |
| `val.hac.lp1.penne…` | login tickets, and **the frontline FQDN** |
| `fro-N.hac.lp1.penne…` | a persistent connection the console holds open |

## npns uses the same frontline, for push notifications

The console's push service (npns, not only Splatoon 3) holds a frontline
too. Its frames, both ways, are a 4-byte little-endian length and a
FlatBuffer. npns does not count as connected — and the friends sysmodule
does not upload presence — until its digital-twin sync ends, which this
server answers by echoing the console's RootHash. After that it pushes
PutRecord notifications: friends' presence changes from the presence bus,
and friend requests/acceptances/removals from BAAS on the `.notify`
channel, one record in flight per connection until the console acks it.
Requests npns sends with a pending id and a 10 s deadline (SubscribeTopic,
PowerState, DAPresence, ReloadLink) are each answered with an Ack carrying
that id; one left unanswered makes npns drop and redial the connection.
The protocol, from a decompile of npns, is in
[`docs/CAPTURES/npns-sysmodule-decompile.md`](../../docs/CAPTURES/npns-sysmodule-decompile.md).

## Nothing here is confirmed against a capture, and that shapes the whole package

The request and response bodies of all three hosts are undocumented. Kinnay's
wiki names the hosts; no public implementation exists; and the reference server
this project studied loads its captures at runtime from a directory the operator
supplies, so its measured bytes are not in it either.

CLAUDE.md rule 6 says what to do: implement the most defensible reading, mark
it, name what a capture would settle. Here the most defensible reading of "we do
not know what to say" is **to say as little as possible** — a well-formed empty
answer rather than an invented Nintendo internal — and **to make the capture
cheap**.

Hence `Options.RecordDir`, which is `internal/wirelog`. Point it at a directory,
put a console through one boot, and bounded request/response bodies land on
disk in the order they happened, hexdumped, with credentials redacted. Long-lived
Frontline streams are the exception: only metadata is recorded, so logging never
consumes the body that the handler must keep reading. The transcript is what
turns every `TODO(capture-needed)` in this package into a one-commit
reconciliation. It is off by default because it writes whatever the console
sends.

```yaml
npln:
  rest_record_dir: "/var/lib/switchnet/captures"
```

## Three decisions worth not undoing

**An unknown host is answered, not refused.** A console that gets a transport
error decides the network is broken, and that surfaces somewhere unrelated to
the request that failed. The same reasoning `internal/stubs` gives.

**Frontline sends an opening frame, then keeps its response open.** The
2026-09-23 successful hall capture contains three empty POSTs, each answered
with a 68-byte size-prefixed FlatBuffer before the response stays open. Only
the Unix timestamp changes between the frames. `frontlineOpeningFrame` builds
that layout from protocol fields; it does not replay captured bytes.

**Request EOF does not end the response.** An empty POST is normal in this
capture. The handler flushes its opening frame immediately, drains any upload
concurrently, and waits for disconnect even after a clean request EOF. HTTP/1
full duplex prevents an unfinished upload from delaying the response. The
listener has no read/write timeout, because the shared thirty-second timeout
would truncate these connections. The meaning of subsequent client messages
and any required acknowledgements remains unconfirmed.

The live capture supersedes the local reference source's silent, EOF-terminated
handler. See [the measured layout and limitations](../../docs/CAPTURES/nextendo-hall-bootstrap-2026-09-23.md).

`Frontlines()` reports how many are open. It exists because it answers the first
question anybody asks when Splatoon 3 will not connect: *did the console get
that far?*

## The frontline FQDN is the one value that must be right

`penne` tells the console where to open its persistent connection. Two failure
modes, both quiet:

- **A field name the console cannot find** leaves it with no frontline at all.
  The FQDN therefore goes out under several plausible spellings at once — extra
  keys in a JSON object cost nothing, and a parser that wants none of them is no
  worse off than with an empty object. This collapses to one field the moment a
  capture shows which.
- **A hostname GRID0+ does not redirect** sends that connection to real
  Nintendo, where it *succeeds*. Nothing fails on this side, and the game then
  misbehaves in a way that looks like GRID0+'s fault while its traffic goes
  somewhere this deployment promises it will not. `New` refuses to start rather
  than allow it, checking `npln.frontline_host` against
  `internal/nintendo.Resolve`.

`fro-N` is a family, not one host, and which member is named is the server's
choice — `hosts.go` covers `fro-1` through `fro-4` in the certificate SANs for
exactly that reason.
