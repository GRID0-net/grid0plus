# internal/rendezvous

A NEX game server: the protocols every NEX title builds on, assembled on
`internal/nex`'s transport. A title (`internal/splatoon2`) configures one,
registers its extensions, and serves it.

On the Switch, one game server is one hostname,
`g<game server id>-lp1.s.n.srv.nintendo.net`, with both halves on one
WebSocket listener, told apart by PRUDP virtual port:

| Port | Half | Protocols |
| --- | --- | --- |
| 1 | authentication | TicketGranting only |
| 2 | secure | everything else, as a named player |

## Endpoints

| Protocol | Methods | Reference |
| --- | --- | --- |
| TicketGranting (10) | ValidateAndRequestTicket (refused: no token), ValidateAndRequestTicketWithCustomData (LoginEx), RequestTicket, GetPID, GetName, ValidateAndRequestTicketWithParam | [Authentication Protocol](https://github.com/kinnay/NintendoClients/wiki/Authentication-Protocol) |
| SecureConnection (11) | Register, RequestConnectionData, RequestURLs, RegisterEx, TestConnectivity, UpdateURLs, ReplaceURL, SendReport | [Secure Protocol](https://github.com/kinnay/NintendoClients/wiki/Secure-Protocol) |

The rest of the protocols live beside these as the title needs them; see
each file.

## Identity

A player's NEX PID **is** their SwitchNet user id, read as a 64-bit number.
The console logs in with that id in decimal as its username (as Kinnay's
Switch examples do), and the id token in its AuthenticationInfo must name the
same user — `internal/identity` decides that, the same check `internal/npln`
makes. A secure connection's PID comes only from its Kerberos ticket.

What is deliberately not here, although NextendoNetwork's server has it:

- no anonymous PID for a username that does not verify;
- no "remember the PID this IP logged in as" for a CONNECT without a ticket;
- no fail-open when the account check cannot be made.

Each of those is the "every console is one person" bug in a different
costume (CLAUDE.md).

A player has one secure connection at a time, and the newest one wins: a
console that lost its connection reconnects before the old one times out.

## Tests

`rvtest` logs consoles in the way a Switch does — id token, LoginEx, ticket,
secure CONNECT — over a real socket. Everything above the transport is tested
through it.
