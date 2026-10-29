# matchmaking

Forms Splatoon 3 matches. A player creates a ticket, watches it on a stream, and gets a game
session once enough others are waiting for the same mode.

| | |
| --- | --- |
| Stands in for | the queue behind `nn.npln.matchmaking.v1.Matchmaker` and `GameSessionService` |
| Served by | `internal/npln/matchmaking.go`, which holds the gRPC side |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Needs a database | no for the queue; yes for the match log |

This package is the queue and the rules. It knows nothing about gRPC, so the wire shapes and the
matching logic can each be read without the other.

## The queue is in memory

A ticket is worth nothing across a restart: the console that owned it has already given up and made
a new one, so persisting tickets would match players against consoles that stopped waiting minutes
ago. Completed matches are a different matter and are persisted, for the dashboard and the record.

## Four rules that each prevent a lobby that never starts

- **One ticket per player.** A second `Enqueue` cancels the first. A console re-entering matchmaking
  after a network blip has forgotten its old ticket, and leaving it queued matches somebody who is no
  longer waiting, so the other consoles sit in a lobby that never fills.
- **Tickets only match within a mode.** People queuing for different things can't play together.
- **A matched ticket can't be cancelled.** The other players are already being told to connect, and
  pulling one back out leaves them waiting for somebody who won't arrive.
- **Tickets time out.** A ticket that never resolves leaves the player on a spinner with no way back.
  Timing out lets the game say "no opponents found" and offer to try again. `Sweep` runs on a timer
  rather than on arrival, because a queue nobody is joining is exactly the one whose tickets need to
  expire.

## Watchers can't stall the queue

`Watch` returns a buffered channel, and a send that would block is dropped. A console that stopped
reading its stream mustn't hold up match formation for everyone else, the same trade
`internal/presencebus` makes. The current state is sent as soon as a watcher attaches, so one that
arrives after its ticket already succeeded doesn't wait for a change that has happened.

## Players per match

`DefaultConfig` is **8**, the full 4v4. For a versus mode it is a cap, not a target, because waiting
for eight players on a server for a handful of consoles would mean never playing:

- **A full lobby starts immediately.**
- **Below the cap, only an even number starts,** so the two teams are equal. An even count of two or
  more starts a countdown (`VersusGracePeriod`, 20 seconds by default). If it runs out with nobody
  arriving or leaving, the match forms with everyone waiting.
- **Any arrival or departure resets it.** An odd count stops the countdown until one more player
  joins, and a new even count starts it again. Someone who queues a second late joins the match
  instead of watching it leave.
- **More than a full lobby** forms one full match at once, and the rest are counted again under the
  same rule.
- **A zero grace period** turns early starts off, so matches form only when full.

A formed match's capacity is the number of players it formed with, so an early four-player match is
full at four.

`Config.ModeSizes` sets a different cap for one mode. Salmon Run is four and Tricolour is 4v2v2.
Salmon Run-shaped modes (`CoopModes`) follow their own rule: no even split, the first player starts `CoopGracePeriod`, every arrival restarts it and a full team forms at
once. They need their size set here, or they take up to eight.

Splatfest modes pick players by fest team (the vote stored with the fest entry, not the ticket's
claim). An ordinary fest match is two teams of equal size. A Tricolor mode is the midterm leader
against the other two teams at 2:1:1, which is 4v2v2 when full, with the defender in `team1` and the
attackers in `team2` and `team3`. `internal/npln` tells the queue who defends with
`SetTricolorDefender`.

The operator's view lists every playable matchmaking config the game links (`internal/npln/modes.go`),
waiting or not, so a size can be set before anyone queues for it.

**The sizes aren't hardcoded.** The key is the mode name the game puts in a ticket's
`matchmaking_config`, and nothing public documents those strings. They live in `npln.mode_players` in
the config, and every mode that arrives without an entry is logged once, which is how the names get
discovered.

## Gameplay is peer to peer

The session names a host and that is all GRID0+ does. Once a match starts, the consoles talk directly
to each other, which is why the addresses handed over must be reachable from the console and why NAT
traversal matters out of proportion to its size.

Splatoon 3 uses ICE (STUN/TURN), not the NAT-check pair, so [`internal/natcheck`](../natcheck/README.md)
isn't involved. `AllocateIceServerSet` hands over the addresses in `npln.ice_servers`, which is empty by
default: consoles on one LAN connect directly. An empty list is a valid answer, not a failure, so
`switchnetctl check` is what warns about it. The failure it causes on hardware is a lobby that fills
and a match that never starts, which reads as a matchmaking bug from the server's side.

## The host's port

Everything else on a `Session` (its players, its host, when it formed) is decided the moment a match
forms. `HostPort` is whatever port the host's own console ends up listening on, which only that console
knows. It starts at zero and `SetHostPort` fills it in later, called from `SyncGameSession` in
`internal/npln/matchmaking.go`, where the rule that only the host may set it also lives. This package
has no notion of who is calling.

## Sessions outlive their tickets, but not forever

A console reads a session id off its ticket stream and only then calls `GetGameSession` and
`JoinGameSession`, so a session has to answer after the match forms. But gameplay is peer to peer, so
nothing ever tells GRID0+ a match ended; the only honest signal is age. `Sweep` drops sessions and
finished tickets older than `SessionTTL`, and skips any ticket that still has a watcher attached,
because deleting one under an open stream strands the console on a stream that will never say anything.

`Join` is idempotent for a player already in the session, because the console joins the match it was
just told it is in, and answering that as a failure would strand it on the loading screen. A session
with no room refuses instead of over-filling, since the game rejects a ninth participant in an
eight-player match. Room is checked against `MaxPlayers`, the size the match formed at, not against
whatever `ModeSizes` says now, so changing a mode's size never shrinks or grows a match that already has
its players.

`Leave` takes a player out of `Players`, because the game never says it is
leaving and a session that kept a gone console handed it to every later
joiner as a peer to connect to (2318-1201). Internal/gamesync decides when a
player has gone; `Leave` only records it. Each player keeps a **seat**
(`Session.Seats`) that does not move when somebody else leaves and comes
back with them if they rejoin: gamesync ranks players by it, Pia elects the
lowest rank as host, and renumbering would give a newcomer the rank a
connected console already holds.

`OpenBackfill` handles a Salmon Run crew that presses "Again!". The game sends
no ticket for it: it reopens the session it just played in and waits there.
Its lobby ready check (`0x71038aab34`) only sends Anarchy and Splatfest
lobbies that are short of players back to matchmaking (`State::ActivateBackFill`).
Salmon Run starts with whoever is in the session, so the open seats are the
server's to fill. While the window is open, the session's capacity is raised to
the mode's size, and waiting tickets in that mode join it before any new job forms
from them, whole squads only and in queue order. `TakeBackfills` reports who
went where; `CloseBackfill` ends it when the lobby locks.
