# internal/rendezvous

A NEX game server: the protocols every NEX title builds on, assembled on
`internal/nex`'s transport. A title (`internal/splatoon2`) configures one,
registers its extensions, and serves it.

On the Switch, one game server is one hostname,
`g<game server id>-lp1.s.n.srv.nintendo.net`, with both halves on one
WebSocket listener, told apart by PRUDP virtual port:

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
| MatchmakeExtension (109) | Open/CloseParticipation, AutoMatchmake_Postpone, AutoMatchmakeWithSearchCriteria_Postpone, AutoMatchmakeWithGatheringId_Postpone, AutoMatchmakeWithParam_Postpone, BrowseMatchmakeSession(NoHolderNoResultRange), Create/JoinMatchmakeSession(WithParam), ModifyCurrentGameAttribute, UpdateNotificationData, Get(lst)FriendNotificationData, UpdateApplicationBuffer, UpdateMatchmakeSession(Attribute/Part), UpdateProgressScore, GetPlayingSession, GetSimplePlayingSession, FindMatchmakeSessionByGatheringId(Detail), FindMatchmakeSessionBySingleGatheringId, FindMatchmakeSessionByOwner, FindMatchmakeSessionByParticipant | [Matchmake Extension Protocol](https://github.com/kinnay/NintendoClients/wiki/Matchmake-Extension-Protocol) |
| Utility (110) | AcquireNexUniqueId(WithPassword), AssociateNexUniqueId(s)WithMyPrincipalId, GetAssociatedNexUniqueId(s)WithMyPrincipalId, GetIntegerSettings, GetStringSettings | [Utility Protocol](https://github.com/kinnay/NintendoClients/wiki/Utility-Protocol) |
| Ranking (112) | methods 1–15: UploadScore, DeleteScore, DeleteAllScores, Upload/Delete/GetCommonData, ChangeAttributes, ChangeAllAttributes, GetRanking, GetApproxOrder, GetStats, GetRankingByPIDList, GetRankingByUniqueIdList, GetCachedTopXRanking(s) | [Ranking Protocol](https://github.com/kinnay/NintendoClients/wiki/Ranking-Protocol) |

A title adds its own methods beside these (`Handle`), and may answer a
whole protocol's unknown methods with `HandleFallback` — Splatoon 2 does for
DataStore; see `internal/splatoon2`.

## Matchmaking

Gatherings are in memory (`matchmaking.go`): a lobby means nothing once its
consoles are gone, and a player leaves every gathering the moment their
connection ends. Pretendo's AGPL matchmaking is the model for the semantics;
where Splatoon 2's proven server behaves differently, that wins and the
comment says so:

- a join is announced to **everyone**, joiner included, and the joiner also
  gets one join per member already there — how the console learns the roster;
- the owner leaving ends a gathering unless its flags ask to migrate;
- search matches only gatherings whose players' NATs can reach the
  searcher's (`NATCompatible`): one strict NAT in a lobby otherwise fails
  everyone in it with 2618-0510;
- the session key is handed only to participants;
- `GetSessionURLs` waits up to 2.5 s for a new host's `ReplaceURL`, instead of
  handing out a station that is about to change.

Friend lookups (`GetPlayingSession`, `GetlstFriendNotificationData`, …) read
the friend list from the store and answer only for friends.

## NAT

Three layers, each off unless it has something to work with:

1. **Households.** Two consoles behind one public address are given each
   other's LAN station: most home routers do not hairpin.
2. **The bridge** (`nattraversal.go`, `bridge`). A console reports its Pia
   socket's *local* port; its router may map another. `cmd/natcheck` saw the
   mapped one and publishes it through Redis (`internal/natobs`); a public
   station handed to a peer carries that port instead. Used only when one
   console is the sole player on its public IP — two on one address cannot be
   told apart.
3. **The relay** (`relay.go`, `nex.relay`). For a pair that still cannot
   punch through, one UDP port per pair on this server, forwarding without
   reading. Only a console's public station is repointed; its LAN station
   keeps the connection id Pia needs. Relayed only for the players
   `nex.relay.allow` names (or everyone, with `allow_all`); a pair is relayed
   when a strict NAT or three failed punches say it needs it, or at once for
   an `!`-prefixed id.

## Persistence

What outlives a connection (unique ids, ranking scores, a title's DataStore
objects) is a `db.NEXRecord` — title, kind, key, owner, JSON value — through
`Records` (`records.go`). One table for every title and protocol, so a
protocol needs no migration of its own.

## Presence

Any secure connection or call marks the console seen (at most every 30 s),
the same liveness signal a BAAS request records: a console that spends the
evening in Splatoon 2 stays online on its friends' lists (`presence.go`).

## Identity

A player's NEX PID **is** their SwitchNet user id, read as a 64-bit number.
The console logs in with that id in decimal as its username (as Kinnay's
Switch examples do), and the id token in its AuthenticationInfo must name the
same user — `internal/identity` decides that, the same check `internal/npln`
makes. A secure connection's PID comes only from its Kerberos ticket.

What is deliberately not here, although NextendoNetwork's server has it:

- no anonymous PID for a username that does not verify;
- no "remember the PID this IP logged in as" for a CONNECT without a ticket;
- no fail-open when the account check cannot be made.

Each of those is the "every console is one person" bug in a different
costume (CLAUDE.md).

A player has one secure connection at a time, and the newest one wins: a
console that lost its connection reconnects before the old one times out.

## Tests

`rvtest` logs consoles in the way a Switch does — id token, LoginEx, ticket,
secure CONNECT — over a real socket. Everything above the transport is tested
through it.
