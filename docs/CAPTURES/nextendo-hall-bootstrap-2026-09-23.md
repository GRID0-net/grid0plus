# Successful Nextendo hall bootstrap, 2026-09-23

## Provenance and scope

The operator started Splatoon 3 v11.3.0, skipped the opening news, entered the
hall and waited about ten seconds on Nextendo. The private capture is preserved
under `raw/nextendo-hall-20260923T205738Z`; the separate second session remains
under `raw/nextendo-second-20260923T210513Z`. Neither raw traffic, credentials,
private keys nor player data belongs in version control. Original-file hashes
are retained privately alongside the first capture's derived analysis.

This report describes the first capture, not Nintendo's official service.
The local Nextendo source differs from the observed live Frontline behavior.
The implementation follows the measured protocol without copying reference code.

## Frontline opening and lifetime

Three `POST /` connections to the Frontline host have empty request bodies.
Each receives HTTP 200, `application/octet-stream`, and a 68-byte opening frame
approximately 0.084, 0.217 and 0.080 seconds after its request. The first two
connections last about 70 seconds; the third ends with capture shutdown.
Request EOF therefore cannot terminate the response. The only changing bytes
in the three opening frames encode Unix seconds matching the server clock.

The frame is a size-prefixed, little-endian FlatBuffer. Offsets below include
the four-byte size prefix; these are field/layout facts, not a replay fixture.

| Absolute offset | Interpretation |
| --- | --- |
| 0 | uint32 payload length 64 |
| 4 | uint32 root offset 20, pointing to table at 24 |
| 12 | vtable size 12, object size 28, field offsets 23/24/16/4 |
| 24 | signed backward vtable distance 12 |
| 28 | uint64 Unix seconds (field 3) |
| 40 | offset 12 to sender string (field 2) |
| 47 | discriminator 12 (field 0; semantic name unconfirmed) |
| 48 | offset 16 to empty payload table (field 1) |
| 52 | length 3 followed by `fro` and NUL |
| 60 | empty payload vtable, size 4 and object size 4 |
| 64 | backward vtable distance 4 |

SwitchNet previously flushed headers but sent no frame and returned on body
EOF. It now constructs this frame using its current clock, flushes immediately,
and holds the response until disconnect. Full duplex also permits an opening
response before an optional upload finishes. Real HTTP tests cover empty and
streamed POSTs, field layout, EOF lifetime, and disconnect cleanup.

The meaning of discriminator 12, later message handling, acknowledgements and
later server cadence remain open. No speculative keepalive frames are added.

## Authenticated current-user alias

Both `Friends/ActivateUser` and `GameRecord/InitializeAttributes` send
`tenants/current/users/current`. SwitchNet formerly compared the last segment
with the authenticated ID and refused the request. Both now resolve this exact
alias to the authenticated caller. Tests reject unauthenticated requests,
explicit other-user requests and foreign-tenant aliases, and verify two users'
attribute documents remain separate.

## The full confirmed call sequence

`analysis/decoded-private.json` (private, git-ignored, in the capture directory)
holds 65 fully decoded exchanges. The gRPC ones, in order, starting immediately
after BAAS login:

```
IssuePrearrangedUserToken
ListLatencyMeasurementServers
LobbyMessaging/RecvMessage        (opens a stream; ~180ms after auth)
LobbyMessaging/SendMessage
Friends/ActivateUser
PresenceService/KeepAlive          (opens a stream)
ListLatencyMeasurementServers      (again)
MaintenanceScheduleService/SubscribeMaintenanceSchedules (opens a stream)
UserScreening/GetViolation
Schedule/SelectVsSchedules, SelectLeagueSchedules, SelectCoopSchedules, SelectSeasonSchedules  (x2)
Friends/SubscribeFriendUsers       (opens a stream)
CloudSave/GetSaveRecord            (toyohr's own, not the OLSC/SCSI one)
Schedule/SelectVsParams
PresenceService/SubscribePresences (opens a stream)
FestService/SelectFestSchedule
Ugcstore/GetDocument               (repeated, several documents)
GameRecord/InitializeAttributes
Canola/SelectDocuments
Locker/SelectDocuments
Ugcstore/RunQuery
... (repeats of the above as the hall stays open)
```

The OLSC/SCSI calls (`/api/console/v3/save_data_archives`, `.../start_upload`)
happen much later, around 40 seconds after hall entry -- not immediately, contrary
to what earlier SwitchNet hardware attempts assumed when investigating that path.

Every one of these RPCs fires in the first ~1.5 seconds after
`IssuePrearrangedUserToken` succeeds. SwitchNet's own hardware attempts have never
observed the client attempt a single one of them -- not `LobbyMessaging`, not
`Friends`, not `Schedule`, nothing -- despite `IssuePrearrangedUserToken` and
`ListLatencyMeasurementServers` both returning OK first. Checked and ruled out as
the reason: neither `LobbyMessaging.RecvMessage`/`SendMessage`,
`UserScreening.GetViolation`, nor `Friends.SubscribeFriendUsers` validate their
request's own `user`/`name` field against the caller's token at all (they read
identity from the token only) -- so the `tenants/current/users/current` alias
bug fixed above cannot be why the client never tries them; that bug could only
matter after a call is attempted, and none ever are.

## Two confirmed shapes, applied

- **Access token TTL is 28800s (8h), not 10800s (3h).** The captured
  `IssuePrearrangedUserTokenResponse`'s access token has `iat`/`exp` exactly
  28800 seconds apart. `npln.access_token_ttl` in both `config.go`'s default
  and `config.example.yaml` changed to match.
- **`ListLatencyMeasurementServers`'s seven-region response matches what
  SwitchNet already ships** (`0fd5a26`'s opt-in experiment): same seven
  `<region>-01-udp` style ids, same region labels, host/port/protocol per
  entry. Already hardware-tested and already ruled out as the hall-stall
  cause (see `splat3-auth-client-trace-2026-09-23.md`); this capture just
  confirms the shape was right all along.

## An open question, not yet acted on: user-id format

Every NPLN user resource name and JWT `sub`/`aid`/`user` field in this capture
uses a `u-` prefix followed by 20 lowercase base32-ish characters, e.g.
`u-4owtixhogqpp3jqpglva`. This is not just Nextendo's own convention: a
decrypted real-Nintendo capture cited elsewhere in this project (see
`internal/npln/auth.go`'s `token_jwt`-derived comments) independently shows the
same `"sub":"u-…"` shape from an actual Nintendo server. SwitchNet's own user
ids are unprefixed hex (`ea4b6873f0397ab3`), with no `u-`.

This is flagged, not fixed: changing the id format touches user creation,
every resource name, every token claim, and the database schema, for a
resource-name/JWT-subject convention that gRPC clients commonly treat as an
opaque string rather than parsing. Given how strict this project has already
found other resource-name parsers to be (`internal/ugcstore`'s README), it is
worth testing narrowly before a wide change: mint one test account with a
`u-`-prefixed id and see whether that alone changes hardware behavior, rather
than migrating every id up front on a guess.

## Limits of the conclusion

The working session proceeds from `IssuePrearrangedUserToken` to latency-list,
messaging, friend activation and presence calls. It does not call `ValidateToken`.
That makes the repeated validation in failing sessions a difference, not proof
that a successful validation must be followed by another validation.

Other observed differences include token claims, STUN attributes and game-data
responses. This change does not establish those as causes or reproduce captured
player values. A new SwitchNet hardware attempt is needed to establish whether
these fixes remove the hall loop and identify the next request, if any, that fails.
