# nplnrest

NPLN's JSON front end: the hosts Splatoon 3 talks to **before** it opens a gRPC channel. This is earlier in
the boot sequence than [`internal/npln`](../npln/README.md), and a console that can't get past it never
reaches the gateway at all.

| | |
| --- | --- |
| Stands in for | `gw.hac.lp1.vermillion.srv.nintendo.net`, `god.hac.lp1.penne.srv.nintendo.net`, `val.hac.lp1.penne.srv.nintendo.net` and `fro-N.hac.lp1.penne.srv.nintendo.net` |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Needs a database | for push notifications only (penne tokens, push channels, tickets) |

| Host | What it does |
| --- | --- |
| `gw.hac.lp1.vermillion...` | device initialisation and account config |
| `god.hac.lp1.penne...` | the push service's penne id and notification tokens, remembered so a push can find the console |
| `val.hac.lp1.penne...` | login tickets, and the frontline hostname |
| `fro-N.hac.lp1.penne...` | a persistent connection the console holds open |

## The same frontline carries push notifications

The console's push service holds a frontline too, not just Splatoon 3. Its frames, both ways, are a 4-byte
little-endian length and a FlatBuffer. The service doesn't count as connected, and the friends system module
doesn't upload presence, until its digital-twin sync ends, which this server answers by echoing the console's
root hash. After that it pushes records: friends' presence changes from the presence bus, and friend
requests, acceptances and removals from BAAS on the `.notify` channel, one record in flight per connection
until the console acknowledges it. Requests the service sends with a pending id and a 10-second deadline
(SubscribeTopic, PowerState, DAPresence, ReloadLink) are each answered with an acknowledgement carrying that
id, and one left unanswered makes the service drop and redial the connection.

## The request and response bodies are not documented

Kinnay's wiki names the hosts but not the bodies, and no public implementation exists to compare against. The
approach is to say as little as possible, a well-formed empty answer rather than an invented internal, and to
make recording cheap. `Options.RecordDir` (see [`internal/wirelog`](../wirelog/README.md)) writes bounded
request and response bodies to disk in order, hexdumped, with credentials redacted. Long-lived frontline
streams record metadata only, so logging never consumes a body the handler has to keep reading. It is off by
default because it writes whatever the console sends:

```yaml
npln:
  rest_record_dir: "/var/lib/switchnet/captures"
```

## Three decisions worth keeping

**An unknown host is answered, not refused.** A console that gets a transport error decides the network is
broken, and that surfaces somewhere unrelated to the request that failed. `internal/stubs` does the same.

**Frontline sends an opening frame, then keeps its response open.** The opening frame is a 68-byte size-prefixed
FlatBuffer in which only the Unix timestamp changes. `frontlineOpeningFrame` builds that layout from protocol
fields instead of replaying bytes.

**A request EOF doesn't end the response.** An empty POST is normal here. The handler flushes its opening frame
immediately, drains any upload concurrently and waits for the disconnect even after a clean request EOF. The
listener has no read or write timeout, because the shared thirty-second timeout would cut these connections off.
What later client messages mean, and whether they need acknowledging, is unconfirmed.

`Frontlines()` reports how many are open. It answers the first question anybody asks when Splatoon 3 won't
connect: did the console get that far?

## The frontline hostname must be right

`penne` tells the console where to open its persistent connection. There are two quiet failure modes.

- **A field name the console can't find** leaves it with no frontline at all. The hostname goes out under several
  plausible spellings at once, since extra keys in a JSON object cost nothing.
- **A hostname GRID0+ doesn't redirect** sends that connection to real Nintendo, where it succeeds. Nothing fails
  on this side, and the game then misbehaves in a way that looks like GRID0+'s fault while its traffic goes
  somewhere this deployment promises it won't. `New` refuses to start rather than allow that, by checking
  `npln.frontline_host` against `internal/nintendo.Resolve`.

`fro-N` is a family, and which member is named is the server's choice. `hosts.go` covers `fro-1` to `fro-4` in
the certificate names for that reason.
