# natobs

Carries the NAT-check responder's observations from `cmd/natcheck` to `cmd/nex` through
Redis (`redis.url`), keyed by the console's public IP. An observation is the external UDP
port a console's router mapped for its peer-to-peer socket.

No Nintendo endpoint lives here. The observation comes from the NAT-check protocol
([`internal/natcheck`](../natcheck/README.md)) and is used by the station bridge in
[`internal/rendezvous`](../rendezvous/README.md). Without `redis.url` nothing is shared and
the bridge is off: stations keep the port the console reported.
