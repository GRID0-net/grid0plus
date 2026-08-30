# internal/matchmaking

Forms Splatoon 3 matches. A player creates a ticket, watches it on a stream,
and gets a game session when enough others are waiting for the same mode.

| | |
| --- | --- |
| Reimplements | the queue behind `nn.npln.matchmaking.v1.Matchmaker` and `GameSessionService` |
| Served by | `internal/npln/matchmaking.go`, which is where the gRPC surface lives |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Needs a database | no (the queue); yes for the match log |

This package is the queue and the rules; it knows nothing about gRPC. The two
services are implemented in `internal/npln`, so the wire shapes and the
matching logic can each be read without the other.

## The queue is in memory, and that is deliberate

A ticket is worth nothing across a restart. The console that owned it has
already given up and made a new one, so persisting tickets would mean matching
players against consoles that stopped waiting minutes ago.

Completed matches are a different matter and are persisted, for the dashboard
and for the record.

## Four rules that each prevent a lobby that never starts

**One ticket per player.** A second `Enqueue` cancels the first. A console
re-entering matchmaking after a network blip has forgotten its old ticket;
leaving it queued matches somebody who is no longer waiting, and the other
consoles sit in a lobby that never fills.

**Tickets only match within a mode.** Two people queuing for different things
are not two people who can play together.

**A matched ticket cannot be cancelled.** The other players are already being
told to connect. Pulling one back out leaves them waiting for somebody who will
not arrive.

**Tickets time out.** A ticket that never resolves shows the player a spinner
with no way back, and they force-quit. Timing out lets the game say "no
opponents found" and offer to try again. `Sweep` is called on a timer rather
than on arrival, because a queue nobody is joining is exactly the one whose
tickets need to expire.

## Watchers cannot stall the queue

`Watch` returns a buffered channel, and a send that would block is dropped. A
console that stopped reading its stream must not hold up match formation for
everyone else — the same trade `internal/presencebus` makes on the request path,
for the same reason.

The current state is sent immediately on attaching, so a watcher that arrives
after its ticket already succeeded does not wait for a change that has happened.

## Players per match

`DefaultConfig` is **2**, not 8. This is a server for a handful of consoles the
operator owns, and waiting for eight players means never playing. The faithful
mode sizes belong in a mode table driven by a capture; until there is one, a
number that lets two friends start a match is more useful than one that is
correct and idle.

## Gameplay is peer-to-peer

The session names a host, and that is all SwitchNet does. Once a match starts
the consoles talk directly to each other and this server is not in the path —
which is why the addresses handed over have to be reachable *from the console*,
and why NAT traversal matters out of proportion to its size.

Splatoon 3 uses ICE (STUN/TURN), not the Pia NAT-check pair, so `internal/natcheck`
is not involved in this title. `AllocateIceServerSet` hands over those addresses
and reads them from `npln.ice_servers`, which is **empty by default**: consoles
on one LAN connect directly, and that is the common case for a private server.

An empty list is a valid answer rather than a failure, so `switchnetctl check`
is what warns about it — the failure it causes on hardware is that the lobby
fills and the match never starts, which reads as a matchmaking bug from this
side. Whether the operator runs coturn at all is recorded in
[`docs/QUESTIONS.md`](../../docs/QUESTIONS.md).

## The host's port is the one piece of state this server cannot compute

Everything else on a `Session` (its players, its host, when it formed) is
decided the moment a match forms. `HostPort` is different: it is whatever
port the host's own console ends up listening on, which only the host's
console can know, so it starts at zero and is filled in later by
`SetHostPort` -- called from `internal/npln/matchmaking.go`'s
`SyncGameSession`, which is also where the rule that only the host may set
it lives. This package has no notion of who is calling, so it trusts
whatever caller reaches `SetHostPort`; enforcing that the caller actually
*is* the host is `internal/npln`'s job, the same place `JoinGameSession`'s
membership check lives.

## Sessions outlive their tickets, but not forever

A console reads a session id off its ticket stream and only *then* calls
`GetGameSession` and `JoinGameSession`, so `Session` has to answer after the
match is formed. But gameplay is peer-to-peer, so nothing ever tells SwitchNet a
match ended — the only honest signal is age. `Sweep` drops sessions and finished
tickets older than `SessionTTL`, and skips any ticket that still has a watcher
attached, because deleting one out from under an open stream strands the console
on a stream that will never say anything again.
