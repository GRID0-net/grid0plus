# Contributing

Thanks for helping. This page covers how to build and test the project, the rules
that are easy to break by accident, and what a good change looks like.

## Setting up

You need Go 1.24 or newer and Docker with Compose.

```sh
make up        # Postgres, Redis and nginx in Docker
make run       # every Go service, in the foreground; Ctrl-C stops them all
make seed      # in another terminal: drives the account chain over HTTP
```

To work on one service without running all of them:

```sh
bin/switchnetctl run baas      # or dauth, aauth, npln, dashboard, ...
```

It builds and runs just that service against the Postgres and Redis you already
have up. All services read the same key directory (`keys.dir` in the config), so
there is nothing extra to set up.

## Before you open a pull request

```sh
make check        # gofmt, go vet, go test
make test-race    # the servers are concurrent, so a data race is a likely bug
make build
```

Then run it. Several bugs in this project were invisible to a passing test suite
and obvious within half a minute of starting the processes. `make up`, `make run`
and `make seed` are cheap.

The unit tests need no database. To also run the integration tests:

```sh
make test TEST_DATABASE_URL="postgres://..." TEST_REDIS_URL="redis://..."
```

If you add a method to the `Store` interface in `internal/db`, add it to the
contract test in `internal/db/store_test.go`. That test runs against both the
PostgreSQL and the in-memory store so they can't drift apart.

If you touch anything that produces a hostname, re-run the generators and check
the output still makes sense:

```sh
SWITCHNET_PUBLIC_IP=192.168.1.50 SWITCHNET_NATCHECK_SECONDARY_IP=192.168.1.51 \
  go run ./cmd/switchnetctl hosts | head
```

Hostnames live in one place, `internal/nintendo/hosts.go`. It generates the DNS
configuration, the nginx map, the certificate names and the hosts file. A host
added anywhere else fails in ways that are hard to trace.

Finally, make sure nothing private is tracked:

```sh
git ls-files | grep -E '\.(pem|key|crt)$|^certs/|config/config\.yaml'   # must print nothing
```

## Rules that are not negotiable

1. **No Nintendo keys, certificates, tickets or dumped files in the repository.**
   Not in test fixtures, not as a temporary constant, not encoded in a comment or
   a document. If a task seems to need one, the approach is wrong. Say so rather
   than finding a way around it. Anything like that is supplied by the operator at
   run time from a file outside the repository.

2. **Don't verify Nintendo-issued credentials, and never forge one.** Device
   certificates, tickets and gamecard challenges are accepted without being
   inspected. That is a design decision, explained in
   [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#what-is-not-verified). Please
   don't "fix" it.

3. **Identity fails closed.** If a lookup can't work out who is calling, the answer
   is an error. There is no default account and no "last user who logged in"
   fallback. Several tests in `internal/baas` exist to hold this line; if you are
   about to weaken one, stop and read [the section on identity](docs/ARCHITECTURE.md#identity).

4. **If a request or response shape is undocumented, don't invent one that looks
   official.** Implement the most defensible reading and mark it with a comment that
   starts `UNCONFIRMED:`. Say what would settle it and which single function to
   change.

## Style

- Comments explain why, not what. The comments worth writing are the ones that
  stop the next person undoing a decision.
- Test names say what breaks: `TestAnIdleConsoleReadsAsOnlineNotOffline`, not
  `TestPresence3`.
- Tests assert on what goes over the wire. Decode tokens and responses by hand
  instead of round-tripping Go structs, because a mis-tagged field would pass a
  round trip and still be wrong on the wire.
- Every package under `internal/` has a `README.md`. If you change what a package
  does, update it.
- Settings go through `config/config.go`, with an environment override and an
  entry in `config.example.yaml`. Unknown YAML keys are rejected on purpose, so a
  typo doesn't silently do nothing. If a wrong value gives a confusing symptom,
  add a check to `switchnetctl check`.

## Commits

One logical change per commit, with a conventional prefix (`feat:`, `fix:`,
`docs:`, `test:`, `refactor:`). The body should say why the change is needed, not
repeat the diff. `git log` shows the usual tone.

## Reporting problems

Open an issue with what you expected, what happened, and the relevant lines from
the service log (`journalctl -u switchnet-<service>`, or the terminal running
`make run`). For anything security related, see [SECURITY.md](SECURITY.md).
