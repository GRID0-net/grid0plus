# internal/jwt

The tokens the Switch auth chain is built on, and the JWK sets that verify them.

| | |
| --- | --- |
| Reference | [Switch Tokens](https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens) |

## The token shapes

| Token | Signed by | Algorithm | Claims that distinguish it |
| --- | --- | --- | --- |
| device | `dauth` | RS256 | `sub` = device id, `aud` = client id, `nintendo` = device info |
| application | `aauth` | RS256 | `sub` = title id, **no `aud`**, `nintendo` = app info |
| BAAS access | `baas` | RS256 | `typ: token`, `bs:grt: 1`, `nintendo` = device info |
| BAAS user | `baas` | RS256 | `typ: token`, `bs:grt: 2`, `bs:did` set |
| BAAS id | `baas` | RS256 | `typ: id_token`, `bs:did` set, `nintendo` = app info |
| NPLN access | `npln` | **ES256** | the game server's own session token (phase 2) |

Every one of them carries `typ: JWT` in the **header**. This table used to say
BAAS tokens had none; that was wrong, and
[CLAUDE.md](../../CLAUDE.md#conventions) records how the mistake survived —
the payload's own `typ` claim (`token` / `id_token`) is a different field, and
genuinely is absent from some tokens. All twelve JWTs in the decrypted capture
of a working server carry the header.

The absences that *are* real still matter as much as the fields: an application
token has no `aud`, an anonymous token no `bs:did`. There is a test for each.

## A key per issuer, never shared

`IssuerDevice`, `IssuerApp`, `IssuerBAASAccess`, `IssuerBAASID` and `IssuerNAID`
each get their own RSA key; `IssuerNPLN` gets its own EC one. Sharing one would
let a token minted for one purpose verify as another — most importantly, an id
token handed to a game server would pass as an access token against the player's
own account.

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

- one algorithm per verifier — `alg: none` is rejected, and so is a token
  signed with the *other* supported algorithm (see below);
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

## ES256, and why it is a separate path

NPLN access tokens are ES256. Nothing else in the stack is, and that is not a
choice — it is what Splatoon 3's own gRPC stack expects (ROADMAP.md §2.2).

`ECKey`, `ECIssuer` and `ECVerifier` sit alongside the RSA types rather than
replacing them. Making `Key` generic over the algorithm would mean touching
`dauth`, `aauth` and `baas`, all of them verified working, to no benefit: an
issuer's algorithm is a property of the protocol it serves, decided once.

**`ECVerifier` is a separate type on purpose.** A single verifier that accepted
either algorithm would let a token signed by the RSA key be presented where an
EC one is expected, and the reverse — the algorithm-confusion attack that
checking `alg` exists to stop. One verifier, one algorithm, one key.
`TestAnRS256TokenIsRefusedByTheES256Verifier` and its mirror assert both
directions.

Two smaller traps, each with a test:

- **The curve is not configurable.** ES256 is ECDSA-on-P-256 by definition
  (RFC 7518 §3.4), so a P-384 key file is refused rather than used to sign
  tokens whose `alg` disagrees with their signature.
- **JWK coordinates are fixed-width.** `big.Int.Bytes()` is minimal-width, so a
  coordinate whose leading byte is zero encodes 31 bytes instead of 32. That
  happens for roughly one generated key in 256, at random — rare enough never to
  appear in a test run, common enough to break a deployment eventually, and it
  looks nothing like a padding bug when it does.
