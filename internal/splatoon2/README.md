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
