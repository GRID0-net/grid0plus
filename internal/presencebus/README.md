# presencebus

Fans presence changes out to whatever wants them, so consumers get events instead of polling
the database.

| | |
| --- | --- |
| Publishers | `internal/baas`, on every presence report |
| Consumers | the dashboard's live "online now", the NPLN presence service |
| Fest entries | `<channel>.fest`: npln publishes each new Splatfest entry (`PublishFestEntry`), the bot API streams it to the Discord bot |

## Two implementations

**`Local`** is in-process. It is correct only when exactly one process publishes presence and
every subscriber lives in it. Leave `redis.url` empty to get it.

**`Redis`** is pub/sub over Redis. It is needed the moment a second process is involved: the
dashboard running separately, the NPLN server, or a second BAAS replica. An in-process bus
across a process boundary delivers **nothing**, silently, so flip the switch before you need
it.

Redis pub/sub is fire-and-forget, which suits presence: an event that arrives late is worthless,
and a subscriber that wasn't connected has nothing to catch up on, because it reads current
state from Postgres when it starts.

## Publishing never blocks

`Publish` runs on the request path of a console's presence report. If it waited for a
subscriber that had stopped reading, the console would hang. So each subscriber gets a
64-event buffer and events past it are **dropped** rather than queued. `Local.Dropped()`
counts them, so a wedged subscriber leaves a trace.

The same reasoning applies in `internal/baas`, which logs a publish failure and carries on. A
presence event that misses the dashboard is cosmetic; a friend-list request that fails because
Redis is down is not.

## Stats are a gauge, not an event

`PublishStats` and `Stats` carry the game server's live matchmaking depth to the dashboard.
They are shaped differently from the event side on purpose.

- **Readable**, because a freshly loaded page needs the current value. A dashboard opened
  between two players queuing would otherwise show nothing until the next one arrived.
- **Replaced, not accumulated.** Two players leaving the queue has to be visible, so a snapshot
  supersedes the last one.
- **Expiring.** A stale gauge is worse than none: "nobody is queuing" and "the game server
  isn't running" look identical on a tile, and only one is worth acting on. `StatsTTL` is short
  compared with how often `internal/npln` republishes, so a running server with an idle queue
  keeps saying "still nobody" and a stopped one is noticed within one TTL.

The Redis implementation uses `SET` with an expiry on a key derived from the channel name, so
two GRID0+ deployments sharing one Redis don't read each other's gauge.
