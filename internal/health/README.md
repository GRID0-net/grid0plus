# health

`/healthz` for every GRID0+ HTTP service, and the checks behind the dashboard's Health
tab.

It stands in for nothing of Nintendo's. `/healthz` is GRID0+'s own path, so it never
collides with a console request.

## Checks, not a flag

A connection that succeeds already says the process is up. What an operator needs to know
is whether the service's dependencies are reachable: a baas that can't see PostgreSQL is
running and useless. Each check asks that question for real.

## Every service gets it

`internal/service.ServeHTTP` mounts `/healthz` ahead of a service's own router, so it is
added in one place and no service can miss it. `scripts/start.sh` waits on it, and the
Toolbox's status screen reads a coarse version of it through `internal/toolbox`.
