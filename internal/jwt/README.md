# internal/jwt

The tokens the Switch auth chain is built on, and the JWK sets that verify them.

| | |
| --- | --- |
| Reference | [Switch Tokens](https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens) |

## The five token shapes

| Token | Signed by | `typ` header | Claims that distinguish it |
| --- | --- | --- | --- |
| device | `dauth` | `JWT` | `sub` = device id, `aud` = client id, `nintendo` = device info |
| application | `aauth` | `JWT` | `sub` = title id, **no `aud`**, `nintendo` = app info |
| BAAS access | `baas` | *none* | `typ: token`, `bs:grt: 1`, `nintendo` = device info |
| BAAS user | `baas` | *none* | `typ: token`, `bs:grt: 2`, `bs:did` set |
| BAAS id | `baas` | *none* | `typ: id_token`, `bs:did` set, `nintendo` = app info |

The absences matter as much as the fields. An application token has no `aud`, and
BAAS tokens have no `typ` **header** (they have a `typ` *claim*, which is a
different thing). Emitting either would not match a capture, and there is a test
for each.

## Four keys, never shared

`IssuerDevice`, `IssuerApp`, `IssuerBAASAccess` and `IssuerBAASID` each get their
own RSA key. Sharing one would let a token minted for one purpose verify as
another — most importantly, an id token handed to a game server would pass as an
access token against the player's own account.

`TestKeyringIssuersHaveDistinctKeys` asserts it.

**Every service must read the same `keys.dir`.** `aauth` verifies `dauth`'s device
tokens and `baas` verifies both. Pointing them at different directories does not
error: it rejects every request with "device token expired", which points nowhere
near the cause.

Key generation uses `O_EXCL`, so when `docker compose up` starts five services at
once on a fresh volume, one wins and the rest load what it wrote. Before that,
each generated its own and kept it in memory while the last writer won on disk.

## What is and is not verified

SwitchNet **only ever verifies its own signature**. A token from real Nintendo
does not verify here, and no attempt is made to verify one: device tokens,
tickets and licences from a console are accepted as opaque strings on a private
network, not validated against Nintendo.

What is enforced, with a test each:

- `RS256` only — `alg: none` is rejected;
- the `kid` must match the verifying key, so a token from another issuer fails
  even though the algorithm matches;
- `exp` is required and enforced;
- an edited claim invalidates the signature.

`ParseUnverified` exists for exactly one case: the Nintendo Account id token in a
`/1.0.0/federation` request, which was issued by a server SwitchNet does not host
and cannot verify. Its output is used as a display label and **never** authorises
anything.

## Key ids

Nintendo uses a random UUID v4. SwitchNet derives a UUID-shaped id from the
public key instead, so it survives a restart and cannot drift out of sync with
the JWK set it is published in, while still looking like what a capture shows.
