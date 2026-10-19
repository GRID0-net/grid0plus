# jwt

The tokens the Switch authentication chain is built on, and the JWK sets that verify them.

Reference: [Switch Tokens](https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens).

## Token shapes

| Token | Signed by | Algorithm | What distinguishes it |
| --- | --- | --- | --- |
| device | `dauth` | RS256 | `sub` is the device id, `aud` the client id, `nintendo` holds device info |
| application | `aauth` | RS256 | `sub` is the title id, **no `aud`**, `nintendo` holds app info |
| BAAS access | `baas` | RS256 | payload `typ: token`, `bs:grt: 1`, `nintendo` holds device info |
| BAAS user | `baas` | RS256 | payload `typ: token`, `bs:grt: 2`, `bs:did` set |
| BAAS id | `baas` | RS256 | payload `typ: id_token`, `bs:did` set, `nintendo` holds app info |
| NPLN access | `npln` | **ES256** | the game server's own session token |

Every token carries `typ: JWT` in its header, BAAS tokens included. The payload's own `typ`
claim (`token` or `id_token`) is a different field and is genuinely absent from some tokens.
The absences that are real still matter as much as the fields: an application token has no
`aud` and an anonymous token has no `bs:did`. There is a test for each.

## A key per issuer, never shared

The device, application, BAAS access, BAAS id and Nintendo Account id issuers each get their
own RSA key, and NPLN gets its own EC key. Sharing one would let a token minted for one
purpose verify as another; most importantly, an id token handed to a game server would pass
as an access token against the player's own account. `TestKeyringIssuersHaveDistinctKeys`
checks it.

**Every service must read the same `keys.dir`.** `aauth` verifies `dauth`'s device tokens
and `baas` verifies both. Pointing them at different directories doesn't error; it rejects
every request with "device token expired", which points nowhere near the cause.

Key generation writes a temporary file and links it into place, so several services starting
at the same moment on a fresh directory converge on one key. A plain exclusive create onto
the final path would publish the name before the contents, and the loser of the race could
read an empty file. See `writeKeyOnce` for the details.

## What is and isn't verified

GRID0+ only ever verifies its own signatures. A token from real Nintendo doesn't verify
here and no attempt is made to verify one. What is enforced, with a test each:

- one algorithm per verifier, so `alg: none` is rejected, and so is a token signed with the
  other supported algorithm;
- the `kid` must match the verifying key, so a token from another issuer fails even when the
  algorithm matches;
- `exp` is required and enforced;
- an edited claim invalidates the signature.

`ParseUnverified` exists for exactly one case: the Nintendo Account id token in a
`/1.0.0/federation` request, which was issued by a server GRID0+ doesn't host. Its output is
used as a display label and never authorises anything.

## Key ids

Nintendo uses a random UUID. GRID0+ derives a UUID-shaped id from the public key instead, so
it survives a restart and can't drift out of step with the JWK set it is published in.

## ES256 is a separate path

NPLN access tokens are ES256, which is what Splatoon 3's gRPC stack expects, and nothing
else in the stack is. `ECKey`, `ECIssuer` and `ECVerifier` sit beside the RSA types rather
than replacing them. An issuer's algorithm belongs to the protocol it serves and is decided
once.

`ECVerifier` is a separate type on purpose. A verifier that accepted either algorithm would
let a token signed by the RSA key be presented where an EC one is expected, and the reverse,
which is the algorithm-confusion attack that checking `alg` exists to stop.

Two smaller traps, each with a test:

- **The curve is fixed.** ES256 is ECDSA on P-256 by definition, so a P-384 key file is
  refused rather than used to sign tokens whose `alg` disagrees with their signature.
- **JWK coordinates are fixed width.** `big.Int.Bytes()` is minimal width, so a coordinate
  whose leading byte is zero encodes as 31 bytes instead of 32. That happens to roughly one
  key in 256, which is rare enough never to show up in a test run and common enough to break
  a deployment eventually.
