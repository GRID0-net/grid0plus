# rendezvous

A NEX game server: the protocols every NEX title builds on, assembled on top of the transport in
[`internal/nex`](../nex/README.md). A title (`internal/splatoon2`) configures one, registers its own
extensions and serves it.

On the Switch, one game server is one hostname, `g<game server id>-lp1.s.n.srv.nintendo.net`, with both
halves on one WebSocket listener, told apart by PRUDP virtual port:

| Port | Half | Protocols |
| --- | --- | --- |
| 1 | authentication | TicketGranting only |
| 2 | secure | everything else, as a named player |

## Endpoints

| Protocol | Methods | Reference |
| --- | --- | --- |
| TicketGranting (10) | ValidateAndRequestTicket (refused: no token), ValidateAndRequestTicketWithCustomData (LoginEx), RequestTicket, GetPID, GetName, ValidateAndRequestTicketWithParam | [Authentication Protocol](https://github.com/kinnay/NintendoClients/wiki/Authentication-Protocol) |
| SecureConnection (11) | Register, RequestConnectionData, RequestURLs, RegisterEx, TestConnectivity, UpdateURLs, ReplaceURL, SendReport | [Secure Protocol](https://github.com/kinnay/NintendoClients/wiki/Secure-Protocol) |
| NATTraversal (3) | RequestProbeInitiation, InitiateProbe, RequestProbeInitiationExt, ReportNATTraversalResult, ReportNATTraversalResultDetail, ReportNATProperties, GetRelaySignatureKey | [NAT Traversal Protocol](https://github.com/kinnay/NintendoClients/wiki/NAT-Traversal-Protocol) |
| Notifications (14) | ProcessNotificationEvent, **sent** by the server; the console never answers | [Notification Protocol](https://github.com/kinnay/NintendoClients/wiki/Notification-Protocol) |
| MatchMaking (21) | UnregisterGathering(s), GetParticipants, GetDetailedParticipants, GetParticipantsURLs, FindBySingleID, UpdateSessionURL, GetSessionURL(s), UpdateSessionHost(V1), UpdateGatheringOwnership, MigrateGatheringOwnership(V1) | [Match Making Protocol](https://github.com/kinnay/NintendoClients/wiki/Match-Making-Protocol) |
| MatchMakingExt (50) | EndParticipation, GetParticipants, GetDetailedParticipants, GetParticipantsURLs | [Match Making Protocol Ext](https://github.com/kinnay/NintendoClients/wiki/Match-Making-Protocol-Ext) |
| MatchmakeExtension (109) | Open/CloseParticipation, the AutoMatchmake variants, BrowseMatchmakeSession, Create/JoinMatchmakeSession, ModifyCurrentGameAttribute, UpdateNotificationData, friend notification data, UpdateApplicationBuffer, UpdateMatchmakeSession, UpdateProgressScore, GetPlayingSession, GetSimplePlayingSession, and the FindMatchmakeSessionBy... lookups | [Matchmake Extension Protocol](https://github.com/kinnay/NintendoClients/wiki/Matchmake-Extension-Protocol) |
| Utility (110) | AcquireNexUniqueId(WithPassword), the AssociateNexUniqueId family, GetIntegerSettings, GetStringSettings | [Utility Protocol](https://github.com/kinnay/NintendoClients/wiki/Utility-Protocol) |
| Ranking (112) | methods 1 to 15: UploadScore, DeleteScore, DeleteAllScores, the common data calls, ChangeAttributes, GetRanking, GetApproxOrder, GetStats, the ranking-by-list calls, and GetCachedTopXRanking(s) | [Ranking Protocol](https://github.com/kinnay/NintendoClients/wiki/Ranking-Protocol) |

A title adds its own methods beside these (`Handle`), and may answer a whole protocol's unknown
methods with `HandleFallback`. Splatoon 2 does that for DataStore; see
[`internal/splatoon2`](../splatoon2/README.md).

## Matchmaking

Gatherings are held in memory (`matchmaking.go`): a lobby means nothing once its consoles are gone. A
player leaves every gathering the moment their connection ends, or the moment they connect again, since
a console that reconnects has a fresh session and doesn't know it was in one. Where Splatoon 2's real
behaviour differs from the general semantics, Splatoon 2's wins and the comment says so:

- a join is announced to **everyone**, joiner included, and the joiner also gets one join per member
  already there, which is how the console learns the roster;
- the owner leaving ends a gathering unless its flags ask to migrate;
- a search matches only gatherings whose players' NATs can reach the searcher's (`NATCompatible`).
  One strict NAT in a lobby otherwise fails everyone in it with error 2618-0510;
- the session key is handed only to participants;
- `GetSessionURLs` waits up to 2.5 seconds for a new host's `ReplaceURL`, instead of handing out a
  station that is about to change.

Friend lookups (`GetPlayingSession`, `GetlstFriendNotificationData` and so on) read the friend list
from the store and answer only for friends.

## NAT

There are three layers, each off unless it has something to work with.

1. **Households.** Two consoles behind one public address are given each other's LAN station, because
   most home routers don't hairpin.
2. **The bridge** (`nattraversal.go`). A console reports its peer-to-peer socket's local port, but its
   router may map another. `cmd/natcheck` sees the mapped one and publishes it through Redis
   ([`internal/natobs`](../natobs/README.md)), and a public station handed to a peer carries that port
   instead. It is used only when one console is the sole player on its public IP, since two on one address
   can't be told apart.
3. **The relay** (`relay.go`, `nex.relay`). For a pair that still can't punch through, one UDP port per
   pair on this server forwards traffic without reading it. Only a console's public station is
   repointed, and its LAN station keeps the connection id the game needs. Relaying applies only to the
   players `nex.relay.allow` names (or everyone, with `allow_all`). A pair is relayed when a strict NAT
   or three failed punches say it needs it, or at once for an id prefixed with `!`.

## Persistence

What outlives a connection (unique ids, ranking scores, a title's DataStore objects) is stored as a
`db.NEXRecord`: title, kind, key, owner and a JSON value, through `Records` (`records.go`). It is one
table for every title and protocol, so adding a protocol needs no migration.

## Presence

Any secure connection or call marks the console seen, at most every 30 seconds. That is the same
liveness signal a BAAS request records, so a console that spends the evening in Splatoon 2 stays online
on its friends' lists (`presence.go`).

## Identity

A player's NEX PID **is** their GRID0+ user id, read as a 64-bit number. The console logs in with that
id in decimal as its username, and the id token in its authentication info must name the same user.
[`internal/identity`](../identity/README.md) decides that, with the same check `internal/npln` makes. A
secure connection's PID comes only from its Kerberos ticket.

Three things are deliberately absent, because each is the "every console is one person" bug in a
different costume:

- no anonymous PID for a username that doesn't verify;
- no "remember the PID this IP logged in as" for a CONNECT without a ticket;
- no fail-open when the account check can't be made.

A player has one secure connection at a time and the newest wins, since a console that lost its
connection reconnects before the old one times out.

## Tests

`rvtest` logs consoles in the way a Switch does (id token, LoginEx, ticket, secure CONNECT) over a real
socket. Everything above the transport is tested through it.
