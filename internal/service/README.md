# service

Shared start-up for every `cmd/*` binary: logging, signal handling, graceful shutdown, and
mounting `/healthz` (see [`internal/health`](../health/README.md)).

It exists so each entry point stays "load config, build one thing, serve". Behaviour that
has to be identical everywhere (how a service logs, how it stops on SIGTERM without
dropping in-flight requests, that it has a health endpoint) is written once here. A change
here restarts every service, because `scripts/update.sh` follows the package graph.
