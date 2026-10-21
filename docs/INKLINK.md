# Run INKLINK, the Grid0+ player companion

INKLINK uses the selected **Afterhours** design. It is a player website, not the
operator dashboard. Players sign in using their existing Grid0 credentials.
See [implemented features and data limits](../internal/inklink/README.md).

## Local demo (no production database)

```sh
GOTOOLCHAIN=go1.24.7 go build -o bin/inklink ./cmd/inklink
bin/inklink -demo
```

Open http://127.0.0.1:8091 and sign in as `demo` / `inklink-demo`.
The banner says **Isolated demo** throughout. This mode creates only an in-memory
sample account/records and an ephemeral signing key. It does not load production
configuration, access Grid0's database, or write Nintendo credentials. Demo state
resets on process restart; browser-local tools keep their own device-local data.

## Connect the real player data

Add or use these supported settings in the existing configuration:

```yaml
inklink:
  listen: "127.0.0.1:8097"
  secure_cookies: true
```

The service reads the same `database`, `keys`, `npln.tenant`, `npln.rotation`,
`baas.image_dir`, and presence TTL settings as the game services. A new optional
service cannot assume another copy of those settings or an operator dashboard
secret. It reads existing records; no database migration is required.

```sh
bin/inklink -config config/config.yaml
```

Environment overrides: `SWITCHNET_INKLINK_LISTEN` and
`SWITCHNET_INKLINK_SECURE_COOKIES`. For local HTTP development **only**, set the
latter to `false`; a Secure cookie intentionally does not work on ordinary HTTP.
Production should use HTTPS and keep it true.

An independent `inklink.pem` signing key is loaded/created under `keys.dir` using
the same atomic keyring as the other services. If key generation was disabled
before INKLINK existed, allow `SWITCHNET_KEYS_GENERATE_IF_MISSING=true` for this
service's first start, then restore the existing policy and back up the key.
Do not commit keys. Losing this key logs browser sessions out but does not
invalidate console credentials.

## Public deployment

The current public endpoint is `https://89.168.58.206:9443`. Its HTTPS
certificate is issued for the IP address and renews automatically. This is not
a Nintendo redirect host and must not be added to the console hosts list. Do
not reuse the operator dashboard's shared token or the Nintendo hostname
certificate setup for public browser authentication.

1. Build `bin/inklink` on the host architecture (the Oracle VM is ARM64).
2. Install `deploy/systemd/switchnet-inklink.service` as an **optional separate
   service**. It is not automatically added to the existing game-services target.
3. The deployed nginx edge loads `deploy/nginx.inklink.public.conf` on port
   9443. Its backend remains on loopback; the HTTP listener serves only ACME
   challenges. The IP certificate lives under `/etc/letsencrypt` and is mounted
   read-only in nginx.
4. Start/enable the optional unit and check `/healthz` locally. Verify real login,
   your own friends/history, an unrelated-account isolation check, and mobile use.
5. Keep `inklink.pem` stable across deployments and rebuild after frontend edits.

The current IP-address certificate uses Let's Encrypt's `shortlived` profile
and lasts six days. Certbot checks twice daily; on renewal the deploy hook
reloads only nginx. The VM's Oracle subnet security list and host firewall must
allow TCP 9443. If the IP changes or a domain is assigned later, change the
nginx `server_name`, certificate request, and the Discord callback together.

The service is included in `make build`; installing or updating its optional
unit and public proxy is a separate deployment step. No live VM route or DNS
record is installed merely by committing this code.

## Feature work remaining outside this release

The original Home menu includes destinations for backend capabilities Grid0 has
not built: gear orders, public X leaderboards, tournament registration/brackets,
replay file hosting and codes, automatic Switch album sync, official Wandercrust
journeys, and comprehensive Story/Side Order progress. INKLINK exposes their
absence rather than fabricating API results. These need explicit data contracts
and providers before they can be completed as real services.

## Discord sign-in and registration

The implementation follows [Discord's authorization-code flow](https://discord.com/developers/docs/topics/oauth2#authorization-code-grant)
and requests **only `identify`** (basic account identity, no email, guilds or
messages). Tokens are used server-side for one identity lookup and are not stored
in the browser, database, or logs. A ten-minute, browser-bound state is consumed
once; restarting INKLINK during authorization means starting sign-in again.

Existing verified Discord links open the **primary Grid0 account**, exactly like
the bot's commands without an account name. Display-name or username matches
never attach accounts. INKLINK never creates an account: a Discord user with
no linked account is sent back with "use /register first", because the bot is
the only way to create one. An account created here once became a player's
primary before they registered with the bot, leaving the bot and their console
on two different identities.
OAuth does not silently
reset an existing player's password or disconnect consoles. A future account
selector can expose secondary linked accounts; currently the primary is used.

### Discord OAuth redirects

The production callback is
`https://89.168.58.206:9443/auth/discord/callback`. Keep it registered in the
existing bot application. The localhost callback
`http://127.0.0.1:8091/auth/discord/callback` is only needed when testing through
the private SSH tunnel described below.

To set up or change OAuth credentials:

1. Open the existing bot application in the [Discord Developer Portal](https://discord.com/developers/applications).
2. Under **OAuth2**, register the exact production callback above. Keep the
   localhost callback only if you still use the private tunnel.
3. Copy the **Client ID** and the existing **OAuth2 Client Secret**. The secret
   is not the bot token. If Discord only offers **Reset Secret**, avoid doing so
   until you know whether another integration uses the existing secret.
4. Store the secret locally, without pasting it into chat or a command:

   ```sh
   mkdir -p data/inklink
   python3 -c 'import getpass,os; p="data/inklink/discord-client.secret"; s=getpass.getpass("Discord OAuth client secret: "); fd=os.open(p,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600); os.fchmod(fd,0o600); os.write(fd,s.encode()); os.close(fd)'
   ```

5. Stop the previous local INKLINK process, then run:

   ```sh
   export SWITCHNET_INKLINK_DISCORD_CLIENT_ID="YOUR_CLIENT_ID"
   export SWITCHNET_INKLINK_DISCORD_CLIENT_SECRET_FILE="$PWD/data/inklink/discord-client.secret"
   export SWITCHNET_INKLINK_DISCORD_REDIRECT_URI="http://127.0.0.1:8091/auth/discord/callback"
   GOTOOLCHAIN=go1.24.7 go build -o bin/inklink ./cmd/inklink
   bin/inklink -demo
   ```

6. Open `http://127.0.0.1:8091`, sign out if needed, and choose **Continue with
   Discord**. Review and authorize the identity permission yourself. In `-demo`
   mode this creates an **isolated sample-database account**, not your live Grid0
   identity; it will have no real game history and disappears on server restart.
   Testing this confirms the Discord round trip, not connection to production.

For live account access, run without `-demo` using the real shared Grid0 database
configuration (see above). Public hosting uses HTTPS and secure cookies. Do not
copy production credentials into committed configuration.

The corresponding YAML block is `inklink.discord` with `client_id`,
`client_secret_file`, and `redirect_uri`. All three have
`SWITCHNET_INKLINK_DISCORD_*` environment overrides. Alternatively supply
`SWITCHNET_INKLINK_DISCORD_CLIENT_SECRET` through a private service environment.
The secret is never supported as plaintext YAML. Secret files must be regular
files with owner-only permissions. The sign-in button stays unavailable until
configured; it never falls back to trusting a supplied Discord ID.

Browser sessions expire on password reset; Discord sessions also stop working
when their Discord ownership link changes. Local motion respects the device's
reduced-motion preference. Schedule boss labels use Cohozuna, Horrorboros and
Megalodontia rather than protocol codenames.

## Private VM access before choosing a hostname

The installed VM companion listens on `127.0.0.1:8097`; port 8091 on that host
already belongs to cloud saves. The isolated local demo intentionally stays on
8091. To access the real VM companion from this PC, stop any local demo and run:

```sh
ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -L 127.0.0.1:8091:127.0.0.1:8097 grid0
```

Keep that terminal open and visit `http://127.0.0.1:8091`. The browser callback
remains `http://127.0.0.1:8091/auth/discord/callback`, so the existing Discord
redirect works through the tunnel. The VM-only service environment is
`/etc/switchnet/inklink.env`; its private secret file lives under
`/opt/switchnet/data/inklink/`. The service is `switchnet-inklink` and starts on
boot. No public port or hostname is needed. `GET /api/auth` reports `demo:false`
for the real service, and browser sessions require a fresh Discord login when
switching from the isolated demo.
