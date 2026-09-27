# internal/health

`/healthz` for every GRID0+ HTTP service, and the checks behind the
dashboard's **Health** tab.

| | |
| --- | --- |
| Reimplements | nothing of Nintendo's — `/healthz` is GRID0+'s own path, so it never collides with a console request |
| Needs a database | no (it reports on the ones a service uses) |

## Why checks, not a flag

A connection that succeeds already says the process is up. What an operator
needs is whether the service's *dependencies* are reachable: a baas that
cannot see PostgreSQL is running and useless. Each check asks that real
question.

## Why every service gets it

`internal/service.ServeHTTP` mounts `/healthz` ahead of the service's own
router, so there is one place it is added and no service can be missed.
`scripts/start.sh` waits on it, and the Toolbox's server-status screen reads a
coarse version of it through `internal/toolbox`.
