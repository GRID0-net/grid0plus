# internal/service

Shared startup for every `cmd/*` binary: logging setup, signal handling,
graceful shutdown, and mounting `/healthz` (`internal/health`).

| | |
| --- | --- |
| Reimplements | nothing of Nintendo's |
| Needs a database | no |

It exists so each entrypoint stays "load config, build one thing, serve".
Behaviour that has to be identical everywhere — how a service logs, how it
stops on SIGTERM without dropping in-flight requests, that it has a health
endpoint — is written once here instead of nine times. A change here restarts
every service (`scripts/update.sh` follows the package graph).
