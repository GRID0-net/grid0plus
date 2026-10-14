# INKLINK player companion

The Afterhours player website. This is a separate service and login surface from
`internal/dashboard`: an operator token must never become a player's credential.
It reimplements no Nintendo endpoint. It reads Grid0's existing Store and
`toyohr` schedule preview functions; it never writes console saves, progression,
friendships, or matchmaking state.

## Tasks and boundaries

1. **Session/API:** implemented. Existing Grid0 username/password, signed
   HttpOnly/SameSite=Strict cookies, login throttling, password-reset/deletion
   checks on every request, and authenticated per-player reads.
2. **Afterhours frontend:** implemented. Desktop sidebar, mobile bottom nav,
   schedules, battle/job details, friends, catalogs and explicit missing-data states.
3. **Artwork:** implemented. Name/ID manifest generated from public Leanny tables;
   stage, weapon, gear and scale images are referenced externally, not vendored.
4. **Local tools:** implemented. Weapon favorites, outfits and UTC preference in
   per-account browser storage; album in IndexedDB; JSON history export; QR text
   detection where the browser supports BarcodeDetector.
5. **Deployment/verification:** local demo and automated tests; optional standalone
   systemd and HTTPS proxy templates. No public hostname is assumed.

Public API: `POST /api/login`, `POST /api/logout`, `GET /api/data`, and
`GET /api/avatar/{id}` (self or current friends). No caller-selected player ID
is accepted on the data endpoint. History queries check owner as well as path.
There is no admin proxy or raw arbitrary-document endpoint.

Cookies use the independent persistent `inklink.pem` key in `keys.dir` and last
30 days. Restarting the process does not log players out. A password reset,
account deletion, or changing that signing key invalidates the browser session.
Logout clears this browser's cookie; it does not disconnect console accounts.
No browser token is stored in localStorage or exposed in a URL.

Only loopback proxy peers may supply X-Real-IP for login throttling. A public
reverse proxy must overwrite that header. POSTs require a same-origin custom
header; CORS is not enabled. Private API responses are not cacheable.

## Available data and honest limits

- VS/Coop history: existing owner-scoped GameRecord documents, newest 200 each.
  Outcome, per-player stats, grade coordinate, eggs and weapon data are read from
  stored records. Missing stats remain unknown, not zero. Averages use only
  timed records with complete required stats and explicitly show their sample.
- Rotations: exactly Grid0's PreviewVsSchedule/PreviewCoopSchedule, including
  operator-authored events. Reward selection uses the serializer's same helper.
- Point card/grades/catalog/scales/wardrobe: existing point-card and attribute
  documents plus a whitelist of the player's uploaded save fields. A missing
  or partial uploaded save means some screens have no values to show.
- SplatNet Shop is a gear browser, **not an ordering service**. Browser-local
  outfits never change in-game equipment. Album photos are device-local, not a
  Switch screenshot sync. Player search searches existing friends, not everyone.
- Global X rankings, tournament brackets, official Wandercrust unlocks, replay
  hosting, and detailed Story/Side Order data have no connected provider. These
  destinations state the limitation. They are not claimed as finished backend
  features. Challenge rotations are available in Schedule; rankings are not.
- QR reads text only, never Nintendo session/login credentials and never follows
  a scanned URL automatically. Browser support varies; a clear fallback is shown.

## Run

See [the operator guide](../../docs/INKLINK.md). The build uses only Go's existing
module dependencies. Frontend assets are embedded: no npm install or runtime
Node process is required. Rebuild after changing those assets.

`go test ./internal/inklink` exercises wire authentication and account isolation.
`node --test internal/inklink/model.test.mjs` checks presentation of real stored
fields, including total-coordinate Salmon Run grades. Node is a test tool only.

Regenerate artwork facts:

```sh
python3 scripts/build-inklink-catalog.py /path/to/Leanny-splat3
```

The manifest credits https://github.com/Leanny/splat3, pins the external asset
revision, and includes no dumped images. Nintendo retains rights in the artwork.
Lucide 0.468.0 is shipped with its MIT license. DM Sans uses Google Fonts.

### Discord authentication

`GET /api/auth` advertises whether Discord login is configured and whether the
server is an isolated demo. `GET /auth/discord` starts a ten-minute, one-use
browser-bound authorization-code flow; `GET /auth/discord/callback` exchanges the
code server-side and resolves the verified Discord ID to its primary Grid0
account. Registration shares `baas.CreateLoginUser` with the bot, including user
defaults and friend-code assignment. Identity never falls back to a matching
username, display name, email or process-wide last user. Unknown Discord users
are explicitly registered; broken or inactive existing links are refused.

Provider tokens and raw errors are not persisted or reflected. OAuth flow
cookies are Lax for the cross-site callback; authenticated session cookies
remain Strict. The callback uses a configured URL rather than a request Host
header. Discord-owned sessions recheck ownership on every request. Tests mock
only the provider transport while exercising real HTTP routes, cookies, signed
sessions and Store behavior. See `docs/INKLINK.md` for operator setup.
