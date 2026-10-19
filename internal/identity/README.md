# identity

Decides who a game server is talking to, from a BAAS id token. Every game server GRID0+
runs shares it:

- `internal/npln` (Splatoon 3), at `IssueToken` and `IssuePrearrangedUserToken`;
- `internal/splatoon2` (Splatoon 2, over NEX), at the ticket-granting login.

No Nintendo endpoint lives here. The token is GRID0+'s own, signed by `internal/baas` with
the `baas_id` key and published at `/1.0.0/certificates`. See the
[BAAS server](https://github.com/kinnay/NintendoClients/wiki/BAAS-Server) page for what a
console does with it.

The rule it holds is that any lookup that can't resolve the caller is an error. There is
no fallback and no default user. [ARCHITECTURE.md](../../docs/ARCHITECTURE.md#identity)
explains why, and the tests here are the same tests that protect BAAS, run once for every
game server.
