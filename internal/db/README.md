# internal/db

Persistence: the schema, the `Store` contract, and two implementations of it.

## What is here

| File | What |
| --- | --- |
| `store.go` | the models and the `Store` interface |
| `postgres.go` | the production store, on `pgx/v5` |
| `memory.go` | an in-memory store, for the handler tests |
| `migrate.go` | the embedded migration runner |
| `migrations/` | numbered SQL, golang-migrate compatible |
| `store_test.go` | one contract test, run against **both** implementations |

## Why an interface and two implementations

`internal/baas`'s handler tests are the tests that matter most — they exercise
what a console actually talks to — so they should run in seconds, on any machine,
in CI, with no container. That means the handlers cannot talk to PostgreSQL
directly.

The risk with a second implementation is that it drifts from the real one and the
handler tests start passing against behaviour production does not have.
`store_test.go` closes that: it is one contract, run against both. It has already
caught a real bug — `FindUsers` with a non-nil empty id filter matched *every*
user under the memory store and none under PostgreSQL, which would have returned
the whole account list as the friend list of anybody with no friends.

The PostgreSQL half of the contract only runs when `SWITCHNET_TEST_DATABASE_URL`
is set. CI sets it.

## Why hand-written queries rather than sqlc

`sqlc` would generate this layer from the SQL, and for a schema this size it
would work. It is not used because:

- it adds a code-generation step to the build, so a contributor cannot change a
  query without installing another tool;
- the queries here are mostly a handful of columns with a `RETURNING`, and the
  two that are not — `UpdateUser` and `UpdatePresence` — build their `SET` clause
  from whichever fields a patch names, which is exactly the dynamic shape sqlc
  does not generate;
- the `Store` interface has to exist anyway for the in-memory implementation, so
  sqlc's generated types would be wrapped rather than used directly.

The migrations keep golang-migrate's file naming, so an operator who prefers that
CLI can point it at `migrations/` and get the same schema.

## The three schema decisions worth knowing

**Friendships are two rows, one per direction.** A single row with an ordered
pair is smaller but wrong: `favorite` is per-direction. A may mark B as a
favourite without B doing the same, and the console shows each side its own flag.
The two rows are always written together in one transaction.

**Presence keeps `reported_at` and `last_seen_at` apart, and `reported_at` is
nullable.** `reported_at` is what the console said; `last_seen_at` is when it
last made any request. NULL means it has never reported, which is *not* the same
as having reported OFFLINE. Conflating any two of those three states is what makes
a friend list either permanently offline or permanently playing —
`migrations/0007_presence.up.sql` has the long version.

**Blocks are one-directional but checked both ways.** Only the blocker sees the
block, but friend requests and presence are suppressed in both directions;
otherwise the blocker could still reach the person they blocked.
