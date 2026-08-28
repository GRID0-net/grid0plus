# internal/matchmaking

Forms Splatoon 3 matches. A player creates a ticket, watches it on a stream,
and gets a game session when enough others are waiting for the same mode.

| | |
| --- | --- |
| Reimplements | `nn.npln.matchmaking.v1.Matchmaker`, `GameSessionService` |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Needs a database | no (the queue); yes for the match log |

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
is not involved in this title. `AllocateIceServerSet` is where those addresses
come from and is **not yet implemented** — it needs a STUN/TURN deployment, which
is an open question for the operator (see `docs/PHASE2_PLAN.md`).
