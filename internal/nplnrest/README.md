# internal/nplnrest

NPLN's JSON front end: the hosts Splatoon 3 talks to **before** it opens a gRPC
channel. This is earlier in the boot sequence than `internal/npln`, and a
console that cannot get past it never reaches the gateway at all.

| | |
| --- | --- |
| Reimplements | `gw.hac.lp1.vermillion.srv.nintendo.net`, `val.hac.lp1.penne.srv.nintendo.net`, `fro-N.hac.lp1.penne.srv.nintendo.net` |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Needs a database | no |

| Host | What it does |
| --- | --- |
| `gw.hac.lp1.vermillion…` | device initialisation, account config |
| `val.hac.lp1.penne…` | login tickets, and **the frontline FQDN** |
| `fro-N.hac.lp1.penne…` | a persistent connection the console holds open |

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

Hence `Options.RecordDir`. Point it at a directory, put a console through one
boot, and every request and response lands on disk in the order it happened,
hexdumped, with credentials redacted. That transcript is what turns every
`TODO(capture-needed)` in this package into a one-commit reconciliation. It is
off by default because it writes whatever the console sends.

```yaml
npln:
  rest_record_dir: "/var/lib/switchnet/captures"
```

## Three decisions worth not undoing

**An unknown host is answered, not refused.** A console that gets a transport
error decides the network is broken, and that surfaces somewhere unrelated to
the request that failed. The same reasoning `internal/stubs` gives.

**The frontline connection sends nothing.** Not as an omission — as the answer.
The frame format is unknown; the reference implementation logs these frames as
opaque hex, so not even it knows what they mean. A console receiving nothing is
a console waiting. A console receiving a malformed frame is one that has decided
the server is broken. Silence is the only state that is certainly valid.

The headers *are* flushed immediately, because a response that has not started
is indistinguishable from a server that is not there.

**And it is never closed from this side.** Closing is an error on the console —
`2307-2103` on Splatoon 2, `2321-*` on Splatoon 3 — not a clean end. That is
also why `cmd/npln` serves this listener with no read or write timeout, through
`service.Serve` rather than `service.ServeHTTP`: the shared thirty-second write
timeout that suits every other service would sever this one, and the player
would see the game lose its connection for no reason.

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
- **A hostname SwitchNet does not redirect** sends that connection to real
  Nintendo, where it *succeeds*. Nothing fails on this side, and the game then
  misbehaves in a way that looks like SwitchNet's fault while its traffic goes
  somewhere this deployment promises it will not. `New` refuses to start rather
  than allow it, checking `npln.frontline_host` against
  `internal/nintendo.Resolve`.

`fro-N` is a family, not one host, and which member is named is the server's
choice — `hosts.go` covers `fro-1` through `fro-4` in the certificate SANs for
exactly that reason.
