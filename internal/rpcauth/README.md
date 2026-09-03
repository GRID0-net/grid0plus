# internal/rpcauth

A context key, and nothing else: the seam that lets `internal/toyohr`'s
`FestService` read the caller identity `internal/npln`'s auth interceptor
already resolved, without the two packages importing each other.

| | |
| --- | --- |
| Reimplements | nothing Nintendo -- this is SwitchNet's own plumbing |
| Served by | written by `internal/npln/server.go`'s `enter`, read by `internal/toyohr/fest_entries.go` |
| Needs a database | no |

## Why this package exists

`internal/npln`'s auth interceptor authenticates every call on its
`*grpc.Server` once, in one place, and stores the result in the request's
`context.Context` under a private key only `npln` can read
(`npln.CallerFrom`). That is deliberately not exported further, for the same
reason nothing in this project trusts a client-asserted identity: a context
key anyone could write to is a fallback waiting to happen.

`internal/toyohr`'s `FestService` is registered on that same `*grpc.Server`
(`internal/npln/services.go`), so it runs behind the same interceptor and
authenticates the same way -- but it is a separate package, and
`internal/npln` already imports `internal/toyohr` to register it. Importing
`internal/npln` back from `internal/toyohr` would be a cycle Go's build
simply refuses, not a style preference to work around.

So `enter` writes the resolved caller to **two** context keys: its own
private one, unchanged, and this package's exported one. Nothing here
authenticates anything on its own -- `With` is documented as npln's alone to
call, the same way a comment is the only thing stopping a handler from
constructing an `internal/npln.Caller` by hand. Go has no way to make a
context key single-writer at the type level; the discipline is the same as
CLAUDE.md's fail-closed rule generally: the one function that resolves an
identity is the one function allowed to assert one.

## If a third package needs this

It doesn't, yet. If a future service package needs the caller the same way
`toyohr` does, it reads `rpcauth.From` the same way -- there is nothing
`toyohr`-specific here. What would be wrong is a second npln-side function
that also calls `rpcauth.With`: there is exactly one place a call gets
authenticated, and it is `enter`.
