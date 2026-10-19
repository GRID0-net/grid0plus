# The Toolbox service

The GRID0+ Toolbox is a homebrew app that sets a console up for GRID0+ (see
[CONSOLE_SETUP.md](CONSOLE_SETUP.md)). The `toolbox` service is the small API that app
talks to. It answers two questions:

- Is there a newer build of the app, and where do I download it?
- Are the GRID0+ services up?

It is optional. Without it the app still works, it just can't update itself or show
server status. The package README, [internal/toolbox](../internal/toolbox/README.md),
describes the endpoints in detail.

## Why a separate service

The app's releases live on GitHub. If the repository is private, an unauthenticated
request from a console would fail, and giving every console a GitHub token would put a
real credential inside a binary anyone can read. So the console asks this service, which
holds the token and asks GitHub on its behalf. The token never leaves your server.

## Setting it up

Do these in order.

### 1. Create a GitHub token

Do this yourself, and never paste the token into a chat or commit it.

1. Go to github.com/settings/personal-access-tokens and generate a **fine-grained**
   token (classic tokens can't be limited to one repository).
2. Under repository access, choose "Only select repositories" and pick
   `GRID0plus-cfw` (owner `GRID0-net`). For an organisation's repository, make the
   organisation the token's resource owner.
3. Under permissions, set **Contents** to read-only and nothing else.
4. Generate it and copy it once.

If the repository is public you can still use a token to avoid GitHub's anonymous rate
limits.

### 2. Configure

In `config/config.yaml`:

```yaml
toolbox:
  listen: "127.0.0.1:8090"
  github_owner: "GRID0-net"
  github_repo: "GRID0plus-cfw"
  github_token: "<the token from step 1>"
  public_base_url: "https://<your public IP>:8443"
  release_cache_ttl_seconds: 120
```

`public_base_url` is the address a console reaches this service at: your public IP,
always port **8443**, always `https`. The service can't work it out from the requests it
receives, because nginx terminates TLS and passes plain HTTP on. Get it wrong and
`/updates/latest` still answers 200, but with a download URL nothing can reach.

You can set these through the environment instead, for example
`SWITCHNET_TOOLBOX_GITHUB_TOKEN` and `SWITCHNET_TOOLBOX_PUBLIC_BASE_URL`. The four
GitHub and URL settings must all be set or all be empty; a partial set is rejected.
Leaving them empty serves `/status` only, and `/updates/*` answers 503.

Validate:

```sh
switchnetctl -config config/config.yaml check
```

### 3. Build and install

```sh
make build                    # builds bin/toolbox
scripts/install-systemd.sh    # installs switchnet-toolbox.service
```

When running natively, `bin/switchnetctl run toolbox` starts it too.

### 4. nginx and the firewall

`docker-compose.yml` already mounts `deploy/nginx.toolbox.conf`. Restart nginx so it
picks up the new listener on port 8443, which reuses the edge certificate:

```sh
docker compose restart nginx
sudo ufw allow 8443/tcp       # or whatever your firewall uses
```

### 5. Start it

```sh
sudo systemctl start switchnet-toolbox.service
sudo systemctl enable switchnet-toolbox.service
```

### 6. Verify

On the server:

```sh
curl -s http://127.0.0.1:8090/healthz
curl -s http://127.0.0.1:8090/status
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8090/updates/latest   # 200 with a token set
```

From outside:

```sh
curl -sk https://<your-public-ip>:8443/status
curl -sk https://<your-public-ip>:8443/updates/latest
```

Check that the `browser_download_url` in the second answer reads
`https://<your-public-ip>:8443/updates/download?asset=...`. If it is `http://` or has no
`:8443`, `toolbox.public_base_url` isn't set to what the running service loaded. The `-k`
is needed because the edge certificate isn't issued for this address, and the app's own
HTTPS client skips verification too.
