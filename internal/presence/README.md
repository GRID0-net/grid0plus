# presence

The time-to-live and visibility rules for a user's online status, shared by everything that
reads it: the HTTP presence endpoints in `internal/baas`, the presence service in
`internal/npln`, and the players view in `internal/dashboard`.

| | |
| --- | --- |
| Stands in for | the read-side rules behind BAAS's presence fields and `nn.npln.friends.v1.PresenceService` |
| Reference | [BAAS Server](https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#presence-extras) |
| Needs a database | no (it reads through the caller's own `db.Store`) |

## Why it is one package

"Friends were never online" was a time-to-live rule implemented wrong, and a third reader of
presence meant a real risk of a third copy quietly disagreeing with the first two. The
dashboard's own copy had already drifted: it collapsed BAAS's two separate lifetimes (the
report and console liveness) into one setting, so an operator who set them differently got a
dashboard that disagreed with the friends applet about who was online.

`Resolve` and `Visible` are the two rules. `Resolver` is a small `db.Store`-backed convenience
around `Resolve` for callers that read presence by user id.

## The two rules

```
fresh report?               use it exactly, including OFFLINE
otherwise seen recently?    ONLINE, app fields cleared
otherwise                   OFFLINE
```

- **A reported state expires.** The console reports `PLAYING` when a game starts and doesn't
  report again. Without a time limit, a console switched off mid-game shows as playing
  forever.
- **Request traffic is proof of life.** A console reports nothing while idling in the Home
  Menu, which is where players look at each other, but it keeps polling. Treating that traffic
  as an `ONLINE` heartbeat is what makes an idle console visible. `internal/npln` extends this
  to every authenticated NPLN call, as `internal/baas` does for HTTP.

A weaker signal never downgrades a stronger, fresher one. A fresh report wins even when it
says `OFFLINE`, because a console reporting that is signing out.

## Visibility belongs to the owner

`Visible` answers "may this viewer see that owner's presence?" from the owner's own
`permissions.presence` (`FRIENDS` by default, `FAVORITE_FRIENDS` or `SELF`).
`internal/npln/friends.go` calls it in both directions to set the friend relationship's
`presence_deliverable` and `presence_receivable`. Get either wrong and a friend's presence
stops showing, or the caller's own stops reaching a friend, while the friend list itself still
looks healthy.
