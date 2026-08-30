# internal/presence

The presence TTL and visibility rules, shared by every reader of a user's
online status: `internal/baas`'s HTTP presence endpoints, `internal/npln`'s
PresenceService, and `internal/dashboard`'s players view.

| | |
| --- | --- |
| Reimplements | the read-side rules behind BAAS's presence fields and `nn.npln.friends.v1.PresenceService` |
| Served by | `internal/baas/presence.go`, `internal/npln/presence_service.go`, `internal/dashboard/server.go` |
| Reference | [BAAS Server](https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#presence-extras) |
| Needs a database | no (reads through the caller's own `db.Store`) |

## Why this is not three implementations

CLAUDE.md documents this repository's shape around two production bugs. One
of them, "friends were never online," is a TTL rule implemented wrong. A
third reader of presence (NPLN's PresenceService) meant a real risk of a
third implementation quietly disagreeing with the first two — which is
exactly how `internal/dashboard`'s own copy had already drifted before this
package existed: it collapsed BAAS's two separate TTLs (report and console
liveness) into one field, so an operator who configured them differently
got a dashboard that could disagree with the friends applet about who was
online.

`Resolve` and `Visible` are the two rules; `Resolver` is a small
`db.Store`-backed convenience around `Resolve` for callers that read
presence by user id rather than already holding a `db.Presence` row.

## The two rules

```
fresh report?            use it exactly, including OFFLINE
otherwise seen recently?  ONLINE, app fields cleared
otherwise                 OFFLINE
```

- **A reported state expires.** The console reports `PLAYING` when a game
  starts and does not report again. Without a TTL, a console switched off
  mid-game shows as playing forever.
- **Request traffic is proof of life.** The console reports nothing while
  idling in the Home Menu — exactly where players look at each other's
  status — but it keeps polling. Treating that traffic as an `ONLINE`
  heartbeat is what makes an idle console visible. `internal/npln/server.go`'s
  `enter()` extends this to every authenticated NPLN call, the same as
  `internal/baas/router.go`'s `authenticated` middleware does for HTTP —
  without it, a console that only ever touches BAAS at login would never
  look alive to PresenceService's own rules.

A weaker signal never downgrades a stronger, fresher one: a fresh report
wins even when it says `OFFLINE`, because a console reporting that is
signing out.

## Visibility is the owner's setting, not the viewer's

`Visible` answers "may viewer see owner's presence", from owner's own
`permissions.presence` (`FRIENDS` by default, `FAVORITE_FRIENDS`, or
`SELF`). `internal/npln/friends.go` calls it in both directions to set
`FriendUser.Relationship`'s `presence_deliverable` and
`presence_receivable` — get either wrong and a friend's presence stops
showing, or the caller's own stops reaching a friend, while the friend list
itself still looks healthy. See `docs/ROADMAP.md#2.5`.
