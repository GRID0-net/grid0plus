# internal/natobs

Carries the NAT-check responder's observations — the external UDP port a
console's router mapped for its Pia socket — from `cmd/natcheck` to
`cmd/nex`, through Redis (`redis.url`), keyed by the console's public IP.

No Nintendo endpoint lives here. The observation comes from the NAT-check
protocol (`internal/natcheck`,
[NAT Check Server](https://github.com/kinnay/NintendoClients/wiki/NAT-Check-Server)),
and is consumed by `internal/rendezvous`'s station bridge. Without `redis.url`
nothing is shared and the bridge is off: stations keep the port the console
reported.
