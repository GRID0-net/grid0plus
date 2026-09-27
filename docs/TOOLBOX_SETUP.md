# Deploying `toolbox`

Setup checklist for the service this branch adds — see
[`internal/toolbox/README.md`](../internal/toolbox/README.md) for what it
does and why it's a separate service, and
[`DEPLOYMENT.md`](DEPLOYMENT.md#toolbox-the-switchnet-toolbox-homebrew-apps-own-api)
for where it fits among the others. This file is the **do-this-in-order**
list for getting it live.

## 1. Generate the GitHub token

**Do this yourself — never paste the token into a chat with an agent, or
commit it anywhere.**

1. github.com/settings/personal-access-tokens → **Generate new token**
   (fine-grained, not classic — classic tokens can't be scoped to one repo).
2. **Repository access**: "Only select repositories" → `grid0plus-toolbox`.
3. **Permissions**: Repository permissions → **Contents: Read-only**.
   Nothing else.
4. Generate, copy the token once.

## 2. Configure

In `config/config.yaml` (not `config.example.yaml` — that one's tracked and
public-shaped):

```yaml
toolbox:
  listen: "127.0.0.1:8090"
  github_owner: "n-popescu"
  github_repo: "grid0plus-toolbox"
  github_token: "<the token from step 1>"
  public_base_url: "https://<your public IP>:8443"
  release_cache_ttl_seconds: 120
```

`public_base_url` is this service's own address as a console reaches it —
same public IP as `network.public_ip`, fixed port **8443** (the nginx block
in step 4 below), always `https`. It is not something `toolbox` can work out
from the request it receives: nginx terminates TLS and proxies to it over
plain HTTP, so the request it actually sees never carries the real scheme or
port. Get this wrong and `/updates/latest` still answers `200`, just with a
`browser_download_url` nothing can reach — see
[`internal/toolbox/README.md`](../internal/toolbox/README.md#updateslatest-and-updatesdownload)
for the exact failure mode this avoids.

Or via environment instead of the file:
`SWITCHNET_TOOLBOX_GITHUB_TOKEN=<token>` and
`SWITCHNET_TOOLBOX_PUBLIC_BASE_URL=<url>` (see `applyEnv` in
`config/config.go` for the other `SWITCHNET_TOOLBOX_*` overrides).

Validate before going further:

```sh
switchnetctl -config config/config.yaml check
```

`config.Validate()` rejects `github_owner`/`github_repo`/`github_token`/
`public_base_url` set partially — if you see that error, one of the four is
missing or misspelled — and rejects a `public_base_url` that isn't an
absolute `https://` URL with no path.

## 3. Build and install the new binary

```sh
make build                    # now also builds bin/toolbox
scripts/install-systemd.sh    # installs deploy/systemd/switchnet-toolbox.service too
```

If you're running services natively instead of via systemd:
`bin/switchnetctl run toolbox` (or it's included automatically in a bare
`switchnetctl run` with no service names — see `runServices` in
`cmd/switchnetctl/run.go`).

## 4. Wire up nginx

`docker-compose.yml` already mounts `deploy/nginx.toolbox.conf` into the
nginx container's `conf.d/` — no compose changes needed. Just restart nginx
so it picks up the new port:

```sh
docker compose restart nginx
```

`deploy/nginx.toolbox.conf` reuses the existing edge certificate
(`certs/edge.crt`/`edge.key`) — nothing new to generate there. It listens on
**8443**.

## 5. Open the port

Whatever firewall sits in front of this box needs **TCP 8443** open to the
consoles, same as 443 and 7575 already are. If you're using `ufw`:

```sh
sudo ufw allow 8443/tcp
```

## 6. Start it

```sh
sudo systemctl start switchnet-toolbox.service
sudo systemctl enable switchnet-toolbox.service   # survive a reboot
```

## 7. Verify

From the server itself:

```sh
curl -s http://127.0.0.1:8090/healthz
curl -s http://127.0.0.1:8090/status
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8090/updates/latest   # 200 once the token is set
```

From outside (or from a console, once grid0plus-toolbox's next release adds the
"Server status" / relay-based updater — see below):

```sh
curl -sk https://<your-public-ip>:8443/status
curl -sk https://<your-public-ip>:8443/updates/latest
```

Check the second response's `browser_download_url`: it must read
`https://<your-public-ip>:8443/updates/download?asset=...`. If it comes back
`http://` and/or missing `:8443`, `toolbox.public_base_url` is not set (or
not the value this box's config actually loaded) — see step 2.

(`-k`: the edge cert isn't issued for this IP/port combination specifically,
and the console's own HTTPS client already skips verification entirely —
see `source/net.c` in grid0plus-toolbox — so a plain browser or `curl` will
warn about the cert too. That's expected here, not a bug.)

## 8. Tell the grid0plus-toolbox side

Once step 7's external checks succeed, say so — the `grid0plus-toolbox` `main`
branch already has the client code for both features (relay-based updater,
"Server status" screen) merged and building cleanly in CI, but the version
was **deliberately not bumped**: cutting a release before this backend
existed would have shipped a build whose update-check and status screen
both fail. Confirming this checklist is what unblocks that version bump and
the release that goes with it.
