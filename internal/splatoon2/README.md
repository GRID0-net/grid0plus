# splatoon2

Splatoon 2's NEX game server, built on [`internal/rendezvous`](../rendezvous/README.md) and served by
`cmd/nex`.

| | |
| --- | --- |
| Host | `g2df33d01-lp1.s.n.srv.nintendo.net:443` (WebSocket, through nginx) |
| Game server id | `2DF33D01` ("main server" in [Kinnay's Game Server List](https://github.com/kinnay/NintendoClients/wiki/Game-Server-List)) |
| Access key | `4eb18d39` (public: it ships in every copy of the game) |
| NEX version | 4.0 (login is `LoginEx`) |
| Title ids | `01003bc0000a0000` (Americas), `0100f8f0000a2000` (Europe), `01003c700009c000` (Japan) |

**Status.** It has been exercised end to end with the NintendoClients library (login, secure
connection, two consoles matched into one lobby with their notifications). It hasn't yet been confirmed
on a console.

## Title-specific choices

- **PRUDP minor version 5 on the login half and 0 on the secure half.** That is what Splatoon 2's
  server answers the secure SYN with.
- **Public station `type=3`, no `Pa`.** That is what Splatoon 2 expects `Register` to answer. Newer
  titles with a newer Pia want `type=11` and `Pa`.
- **The secure server is on the same host as the login.** The console is told `address=0.0.0.1`, which
  a Switch reads as "where you already are". `nex.splatoon2.secure_address` and `secure_port` move it
  if a console doesn't honour that.

## Endpoints

On top of everything `internal/rendezvous` serves (login, secure connection, NAT traversal,
matchmaking, notifications, Utility and the standard Ranking):

| Protocol | Methods | Reference |
| --- | --- | --- |
| Utility (110) | 9 AcquireTagId, 10 UpdateCurrentUser | [Utility Protocol (Splatoon 2)](https://github.com/kinnay/NintendoClients/wiki/Utility-Protocol-(Splatoon-2)) |
| MatchmakeExtension (109) | 60 CustomGetSimplePlayingSession: which lobby each friend is in, for friend-join | [Matchmake Extension Protocol (MK8D)](https://github.com/kinnay/NintendoClients/wiki/Matchmake-Extension-Protocol-(MK8D)), same id and shape |
| Ranking (112) | 16 UploadLeaguePoint, 17 GetLeagueResult, 18 GetFestivalResult, 19 UploadFestivalVote, 20 UploadFestivalScore, 21 DeleteFestival, 22 UploadXPower, 23 GetXPowerRanking, 24 UploadEventMatchResult, 25 GetEventMatchResult | [Ranking Protocol (Splatoon 2)](https://github.com/kinnay/NintendoClients/wiki/Ranking-Protocol-(Splatoon-2)) |
| DataStore (115) | 51 to 54, 59 to 65 and 68 match result uploads (every mode and revision), 69 UploadCoopResult, 55 GetOrderedGear, 56 PurchaseGear, 57 UploadTimeAttack, 47 and 58 CoconutRegisterMeta(ByParam), 48 CoconutRatePost, 49 CoconutGetObjectInfos, 50 CoconutReportViolation, 66 PreparePostPlayLog, 67 PrepareGetPlayLog | [DataStore Protocol (Splatoon 2)](https://github.com/kinnay/NintendoClients/wiki/Data-Store-Protocol-(Splatoon-2)) |

Any other DataStore method gets an empty success and a warning naming it. The game carries on after
that, whereas `NotImplemented` isn't known to be survivable. Each such log line is a method to
implement properly.

Two plain HTTP paths ride on the same host, for the object URLs DataStore hands out:
`POST /datastore/playlog/<id>` (a play-log upload this server prepared) and
`GET /datastore/coconut/<id>` (a plaza post's data).

## What it does with them

- **Match history.** A lobby that becomes a match is filed in GRID0+'s match history (tenant
  `splatoon2`), so the dashboard's Matches tab shows Splatoon 2 beside Splatoon 3. Each uploaded result
  is kept with its mode, rule and stage.
- **Splatfests.** Votes and scores are tallied per team, and `GetFestivalResult` answers from the tally.
- **League.** A team is identified by `AcquireTagId` (the same id for the same members in any order)
  and ranked against the other teams of its league.
- **Annie's gear orders.** The SplatNet 2 app is gone, so the operator places an order from the
  dashboard's Annie tab and the game picks it up at the shop.
- **Plaza posts.** A post is stored with its binary and served back to other players. A reported post is
  hidden from everyone but its author.

Shapes that are guesses are marked `UNCONFIRMED:` in the source.

## Operator API

Served on `nex.admin_listen` and only when `nex.admin_token` is set.

| Route | What |
| --- | --- |
| `GET /splatoon2` | the live status: connected players (address, NAT, lobby, open order), lobbies, recent results, every method called with counts, and the relay's pairs |
| `GET /splatoon2/fests` | every Splatfest voted or scored in, with its tally |
| `GET /splatoon2/leagues` | every league, with its teams ranked by best point |
| `GET /splatoon2/plaza` | every plaza post, without its image |
| `GET /splatoon2/gear-orders` | every order Annie is holding, connected or not |
| `GET /splatoon2/uploads` | X Power, event match and Octo Expansion uploads kept unread |
| `POST /splatoon2/players/{id}/gear-order`, `DELETE` on the same path | place or cancel an order |
| `POST /splatoon2/players/{id}/disconnect` | end a console's connection |
| `POST /splatoon2/plaza/{dataId}/hidden` | hide or show a plaza post |

PIDs travel as decimal strings, because a JSON number above 2^53 would reach the page already rounded to
another player. The dashboard's Splatoon 2 side reads all of it (`dashboard.nex_admin_url` and
`dashboard.nex_admin_token`).
