# db

Persistence: the schema, the `Store` contract and two implementations of it.

| File | What |
| --- | --- |
| `store.go` | the models and the `Store` interface |
| `postgres.go` | the production store, on `pgx/v5` |
| `memory.go` | an in-memory store, used by the handler tests |
| `migrate.go` | the embedded migration runner |
| `migrations/` | numbered SQL, golang-migrate compatible |
| `store_test.go` | one contract test, run against **both** implementations |

## Why an interface and two implementations

The handler tests in `internal/baas` are the ones that matter most, because they exercise
what a console actually talks to. They should run in seconds, on any machine, in CI, with
no container, so the handlers can't talk to PostgreSQL directly.

The risk with a second implementation is that it drifts from the real one and the tests
start passing against behaviour production doesn't have. `store_test.go` closes that gap:
it is one contract, run against both. It has already caught a real bug, where `FindUsers`
with an empty id filter matched every user in the memory store and none in PostgreSQL.

The PostgreSQL half of the contract only runs when `SWITCHNET_TEST_DATABASE_URL` is set.
CI sets it. If you add a `Store` method, add it to the contract.

## Why hand-written queries rather than sqlc

sqlc would work for a schema this size, but it adds a code-generation step to the build,
the two queries that matter most (`UpdateUser` and `UpdatePresence`) build their `SET`
clause from whichever fields a patch names, which is exactly the shape sqlc doesn't
generate, and the `Store` interface has to exist anyway for the in-memory store.

Migrations keep golang-migrate's file naming, so you can point that CLI at `migrations/`
and get the same schema.

## Three schema decisions

- **Friendships are two rows, one per direction.** `favorite` is per direction, so A can
  mark B as a favourite without B doing the same. The two rows are always written in one
  transaction.
- **Presence keeps `reported_at` and `last_seen_at` apart, and `reported_at` is
  nullable.** NULL means the console has never reported, which isn't the same as having
  reported offline. Mixing those states is what makes a friend list permanently offline or
  permanently playing.
- **Blocks are one-directional but checked both ways.** Only the blocker sees the block,
  but friend requests and presence are suppressed in both directions.
