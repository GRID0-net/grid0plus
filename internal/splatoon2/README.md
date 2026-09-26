# internal/splatoon2

Splatoon 2's NEX game server, built on `internal/rendezvous` and served by
`cmd/nex`.

| | |
| --- | --- |
| Host | `g2df33d01-lp1.s.n.srv.nintendo.net:443` (WebSocket, via nginx) |
| Game server id | `2DF33D01` ("main server" in [Kinnay's Game Server List](https://github.com/kinnay/NintendoClients/wiki/Game-Server-List)) |
| Access key | `4eb18d39` (public: it ships in every copy of the game) |
| NEX version | 4.0 (login is `LoginEx`) |
| Title ids | `01003bc0000a0000` (Americas), `0100f8f0000a2000` (Europe), `01003c700009c000` (Japan) |

## Title-specific choices

- **PRUDP minor version 5 on the login half, 0 on the secure half.**
  NextendoNetwork measured Splatoon 2's proven server answering the secure
  SYN with 0, the client held constant (facts only).
- **Public station `type=3`, no `Pa`.** Same source: that is what Splatoon 2's
  proven server answers `Register` with. Newer Pia (Smash) wants `type=11`
  and `Pa`; Splatoon 2's Pia is older.
- **The secure server is on the same host as the login.** The console is told
  `address=0.0.0.1`, which Kinnay documents a Switch reading as "where you
  already are". `nex.splatoon2.secure_address`/`secure_port` move it if a
  console turns out not to honour that.

## Endpoints

On top of everything `internal/rendezvous` serves (login, secure
connection, NAT traversal, matchmaking, notifications, Utility, standard
Ranking):

| Protocol | Methods | Reference |
| --- | --- | --- |
| Utility (110) | 9 AcquireTagId, 10 UpdateCurrentUser | [Utility Protocol (Splatoon 2)](https://github.com/kinnay/NintendoClients/wiki/Utility-Protocol-(Splatoon-2)) |
| MatchmakeExtension (109) | 60 CustomGetSimplePlayingSession — which lobby each friend is in, for friend-join | [Matchmake Extension Protocol (MK8D)](https://github.com/kinnay/NintendoClients/wiki/Matchmake-Extension-Protocol-(MK8D)), same id and shape |
| Ranking (112) | 16 UploadLeaguePoint, 17 GetLeagueResult, 18 GetFestivalResult, 19 UploadFestivalVote, 20 UploadFestivalScore, 21 DeleteFestival, 22 UploadXPower, 23 GetXPowerRanking, 24 UploadEventMatchResult, 25 GetEventMatchResult | [Ranking Protocol (Splatoon 2)](https://github.com/kinnay/NintendoClients/wiki/Ranking-Protocol-(Splatoon-2)) |
| DataStore (115) | 51–54, 59–65, 68 match-result uploads (every mode and revision), 69 UploadCoopResult, 55 GetOrderedGear, 56 PurchaseGear, 57 UploadTimeAttack, 47/58 CoconutRegisterMeta(ByParam), 48 CoconutRatePost, 49 CoconutGetObjectInfos, 50 CoconutReportViolation, 66 PreparePostPlayLog, 67 PrepareGetPlayLog | [DataStore Protocol (Splatoon 2)](https://github.com/kinnay/NintendoClients/wiki/Data-Store-Protocol-(Splatoon-2)) |

Any other DataStore method gets an empty success and a warning naming it:
NextendoNetwork's server answers every DataStore method that way and the
game proceeds (facts only), while NotImplemented is not known to be
survivable. Each such log line is a method to implement properly.

Two plain-HTTP paths ride on the same host, for the object URLs DataStore
hands out: `POST /datastore/playlog/<id>` (a play-log upload this server
prepared) and `GET /datastore/coconut/<id>` (a plaza post's data).

## What it does with them

- **Match history.** A lobby that becomes a match is filed in SwitchNet's
  match history (tenant `splatoon2`), so the dashboard's Matches tab shows
  Splatoon 2 beside Splatoon 3. Each uploaded result is kept with its mode,
  rule and stage.
- **Splatfests.** Votes and scores are tallied per team;
  `GetFestivalResult` answers from the tally.
- **League.** A team is identified by `AcquireTagId` (the same id for the same
  members in any order) and ranked against the other teams of its league.
- **Annie's gear orders.** The SplatNet 2 app is gone; the operator places an
  order from the dashboard's Splatoon 2 tab, and the game picks it up at the
  shop.
- **Plaza posts.** A post is stored with its binary and served back to other
  players; a reported post is hidden from everyone but its author.

Every guessed shape is a `TODO(capture-needed)`, listed in
[`docs/CAPTURES/README.md`](../../docs/CAPTURES/README.md#splatoon-2-nex).

## Operator API

`nex.admin_listen` with `nex.admin_token` (`admin.go`): the connected players
(address, NAT, lobby, open order), the lobbies, recent results, every method
called with counts, the relay's pairs; and controls to disconnect a player
or place and cancel a gear order. The dashboard's Splatoon 2 tab reads it
(`dashboard.nex_admin_url`/`nex_admin_token`). Not served without a token.
