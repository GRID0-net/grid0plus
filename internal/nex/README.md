# internal/nex

NEX, the protocol stack a Switch game uses to talk to its own NEX game server.
This package is the game-agnostic part: how bytes are laid out and how a
connection is kept. What a particular game's server answers lives elsewhere
(`internal/splatoon2`).

On the Switch, NEX is:

```
WebSocket over TLS (wss://g<game server id>-lp1.s.n.srv.nintendo.net:443)
  └─ PRUDP "Lite" packets       (prudp.go)
       └─ RMC calls             (rmc.go)
            └─ NEX-encoded values (stream.go, station.go)
```

References, in the order they were used:

- [NEX overview](https://github.com/kinnay/NintendoClients/wiki/NEX-Overview-(Game-Servers))
- [NEX common types](https://github.com/kinnay/NintendoClients/wiki/NEX-Common-Types)
- [PRUDP](https://github.com/kinnay/NintendoClients/wiki/PRUDP-Protocol), Lite format
- [RMC](https://github.com/kinnay/NintendoClients/wiki/RMC-Protocol)
- [Kerberos authentication](https://github.com/kinnay/NintendoClients/wiki/Kerberos-Authentication)
- Kinnay's own client and server in
  [NintendoClients](https://github.com/kinnay/NintendoClients) (`nintendo/nex`),
  and PretendoNetwork's AGPL [nex-go](https://github.com/PretendoNetwork/nex-go)
  as a cross-check.

## Only the Switch profile

8-byte PIDs, a version header on every structure, PRUDP Lite with no payload
encryption (the TLS tunnel provides it), the "new" Kerberos key derivation.
None of these is negotiated on the wire, so there is nothing to gain from
carrying the 3DS/Wii U variants.
