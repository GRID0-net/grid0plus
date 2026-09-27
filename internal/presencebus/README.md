# internal/presencebus

Fans out presence changes to whatever wants them, so consumers get events rather
than polling the database.

| | |
| --- | --- |
| Publishers | `internal/baas`, on every presence report |
| Consumers | phase 2: the dashboard's live "online now", the NPLN presence service |

## Two implementations

**`Local`** is in-process. It is correct when exactly one process publishes
presence and every subscriber lives in it — which is all of phase 1, since only
the BAAS server writes presence. Leave `redis.url` empty to get it.

**`Redis`** is pub/sub over Redis. Needed the moment a second process is
involved: the phase 2 dashboard running separately, the NPLN server, or a second
BAAS replica. An in-process bus across a process boundary delivers **nothing**,
silently, so this is a switch worth flipping before it is needed rather than
after.

Redis pub/sub is fire-and-forget, which suits presence exactly: an event that
arrives late is worthless, and a subscriber that was not connected has nothing to
catch up on because it reads current state from Postgres when it starts.

## Publish must not block

`Publish` runs on the request path of a console's presence report. If it blocked
waiting for a subscriber that had stopped reading, the console would hang.

So a subscriber gets a 64-event buffer, and events are **dropped** past it rather
than queued. `Local.Dropped()` counts them, so a wedged subscriber leaves a trace
instead of quietly losing updates.

The same reasoning applies one level up: `internal/baas` logs a publish failure
and swallows it. A presence event that misses the dashboard is cosmetic; a
friend-list request that fails because Redis is down is not.

## Stats is a gauge, not an event

`PublishStats`/`Stats` carry the game server's live matchmaking depth to the
dashboard, and they are deliberately shaped differently from the event side.

**Readable, because a freshly-loaded page needs the current value.** An event
stream carries changes; a dashboard opened between two players queuing would show
nothing until the next one arrived.

**Replaced, not accumulated.** Two players leaving the queue has to be visible,
so a snapshot supersedes the last one rather than adding to it.

**Expiring.** A stale gauge is worse than an absent one: "nobody is queuing" and
"the game server is not running" look identical on a tile, and only one of them
is worth acting on. `StatsTTL` is short relative to how often `internal/npln`
republishes, so a running server with an idle queue keeps saying "still nobody"
and a server that stopped is noticed within one TTL.

The Redis implementation uses `SET` with an expiry on a key derived from the
channel name — derived, so two GRID0+ deployments sharing one Redis do not
read each other's gauge, which would be a confusing way to find out the channels
were shared.
