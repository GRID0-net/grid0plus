# internal/identity

Who a game server is talking to, decided from a BAAS id token. Shared by every
game server SwitchNet runs:

- `internal/npln` (Splatoon 3) at `IssueToken` / `IssuePrearrangedUserToken`;
- `internal/splatoon2` (Splatoon 2, NEX) at the ticket-granting login.

No Nintendo endpoint lives here. The token is SwitchNet's own, signed by
`internal/baas` with the `baas_id` key and published at
`/1.0.0/certificates` — see
[BAAS server](https://github.com/kinnay/NintendoClients/wiki/BAAS-Server) for
what the console does with it.

The rule it holds: any lookup that cannot resolve the caller is an error. No
fallback, no default user. CLAUDE.md's "Every console acted as one person"
section is the reason; the tests here are that section's tests, once for every
game server.
