# The NPLN session token a match hands a console (2026-09-23)

| | |
| --- | --- |
| Hosts | the NPLN tenant (`t-….npln.srv.nintendo.net`), `gamesync.npln.nintendo.net` |
| Title | Splatoon 3 (11.2.0 / 11.3.0) |
| Evidence | a hardware capture of a **working** server, published by [Nextendo Network](https://nextendo.network) in [NextendoNetwork/splatoon-3](https://github.com/NextendoNetwork/splatoon-3) (`token_jwt.go`, `matchmaking.go`) |
| Captured by | **not this project** — second-hand, and recorded here as second-hand |

Not a packet capture in this directory's usual sense. SwitchNet holds no capture
of its own here and none of the bytes below came from a console under this
project's control. What earns it a file is that it answers a question the source
was carrying as `TODO(capture-needed)`, and the answer was measured on hardware
by the only other project running Splatoon 3's NPLN client against a server it
controls.

## What it settles

The token in `MatchedUserSession.matchmaking_id_token` — the field a match hands
each player — is **required, and the console is its reader**. SwitchNet left it
empty on the reasoning that nothing verified it. That was true of the server, and
irrelevant to the client, which reads it before sending it anywhere.

The captured token:

```
header  {"alg":"ES256","kid":"754fe17e-…"}           two members: no jku, no typ
payload {exp, iat, iss:"gss", sub, gamesync:{…}}     exp - iat = 3600
        gamesync: attr gsid ltcy team tid typ uid usid
```

and the cost of leaving it out, in that project's own words: the client opens
TCP+TLS+h2 to the session host and **never emits an HTTP/2 HEADERS frame**, the
connection falls to EOF after about a second, and the game reports **`2321-4992`
on `KeepUserSession`**. From the server side nothing was ever called, which is
why the failure leaves no trace in the server's own log — the shape of bug the
empty field was.

## What it does not settle

- **Whether a console accepts the token SwitchNet signs.** Nothing here was
  produced by SwitchNet's key: the *shape* is what was measured, not this
  server's instance of it. Only a run on a real console can show that.
- **`team`** — a Splatfest team. SwitchNet announces fests but assigns no team,
  so the field goes out empty rather than carrying an invented placement.
- **`attr` and `ltcy`** — the client's own attribute and latency JSON, which the
  capture shows as `{}` and `{"latencies":{}}` on a session that has gathered
  neither. Whether real values change anything is unconfirmed.
- **The two-member header.** Whether any client checks it is unknown; SwitchNet
  emits the measured shape rather than assuming the check does not exist.

## Where it is implemented

- `internal/npln/auth.go`, `issueSessionToken` — one token per matched player,
  ES256, signed with the same key every other NPLN token uses.
- `internal/npln/matchmaking.go`, `matchedUserSessions` — puts it in the ticket;
  a signing failure fails the call rather than handing out a match half its
  players cannot open.
- `internal/npln/auth.go`, `VerifyMatchmakingIDToken` — accepts it back at
  gamesync. `iss` `"gss"` plus the `gamesync` block is what separates it from an
  access token, now that neither carries an audience.
- `internal/jwt/ec.go`, `SignSessionToken` — the two-member header.
- `internal/npln/session_token_test.go` — the shape, asserted on the wire and
  decoded by hand.
