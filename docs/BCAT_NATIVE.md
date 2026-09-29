# Serving BCAT natively — design plan

For the operator steps to build, stage, and announce a Splatfest, see
[SPLATFEST_ACTIVATION.md](SPLATFEST_ACTIVATION.md).

**Status:** the opt-in title seed reader and list/data HTTP path are
implemented. Signature patch and real-hardware acceptance remain open; the
later sections retain the original design reasoning and pending checks.

**Goal:** a console pointed at GRID0+ downloads BCAT delivery-cache
content — Splatoon 3 Splatfest packs, Splatoon 2 schedule seeds, and
anything else a title fetches through `nn::bcat` — from GRID0+'s own
`bcat-list` / `bcat-data` hosts, the same way it downloads them from
Nintendo. No homebrew installer, no files handed to the player, no save
editing. This replaces `docs/TODO.md` §2.4a's `.nro` installer as the
primary path; keep the installer as the fallback for un-patched consoles.

This plan assumes the reader has read `docs/TODO.md` §2.4 (the RE findings
and wire format) and `internal/bcat/README.md`. It does not repeat the byte
layouts; it references them.

---

## 1. What is already settled (do not re-derive)

From a public client of the live servers (BCAT-Manager, facts only) and a
2026-09-26 disassembly of `bcat.nso` 22.5.0 (build id `6D9772A733E2370B…`,
extracted with `switchnet_bcat_extract.py`, read in Ghidra):

1. **Endpoints** (`bcat.nso` `.rodata`, confirmed):
   - list: `https://<bcat-list host>/api/nx/v1/list/nx_data_<title id, 16
     lowercase hex>` — the `nx_data_%016llx` and
     `https://%s/api/nx/v1/list/%s%s%s` format strings are both present.
   - data: each list entry carries an absolute `url`. Whether it must be a
     `bcat-data-lp1` host is **open** — see §7.
   - The request carries `User-Agent: libcurl (nnBcat; <uuid>; SDK
     <a.b.c.d>)`, `Accept: */*`, and `X-Nintendo-Serial-Number`. No BAAS/
     dauth token. **BCAT needs no GRID0+ auth.**
2. **The list body is itself a `bcat` container**; its decrypted payload is
   MessagePack (matches the on-disk cache name `list.msgpack`). Field names
   are in `internal/bcat/probe.go`'s comment and §2.4. Top level:
   `topic_id`, `service_status`, `na_required`,
   `required_application_version`, `directories[]`; each directory has
   `name`, `mode`, `by_country_group`, `digest`, `data_list[]`; each data
   entry has `data_id`, `filename`, `url`, `digest`, `size`.
3. **Container header (0x120 bytes)** — layout in §2.4. Key fields: magic
   `bcat`, version `1`, crypto type at 0x5, signature-hash type at 0x6,
   secret index at 0x7, CTR IV at 0x10, RSA-2048 signature at 0x20, payload
   at 0x120.
4. **The signature is verified and enforced.** After decrypt, `bcat.nso`
   runs RSA-2048 verify over the payload against a public key baked into
   the module (production and development keys, chosen by the
   `bcat!production_mode` setting). Failure wipes the buffer and returns
   result **2002-4106**. Both the whole-file and streaming paths do it.
   *This is the one hard blocker.* It is answered by a `bcat.nso` patch, not
   by anything on the server (see §4).
5. **Plaintext containers are accepted.** The header validator accepts
   crypto type 0; the cipher setup treats 0 as "no encryption" (1/2/3 are
   AES-128/192/256-CTR). So a GRID0+ container can be **crypto type 0**
   and skip PBKDF2, the passphrase, and the module's secret strings
   entirely. Only the signature stands between a plaintext container and the
   game.

**Consequence for the design:** GRID0+ serves *plaintext, unsigned*
containers (crypto 0), and a `bcat.nso` ExeFS patch makes the console accept
them. No Nintendo key is ever needed, held, or forged — same posture as the
Splatoon 3 TLS patch this repo already ships (rule 2 intact).

---

## 2. The two independent deliverables

These are separable and can land in either order; native serving needs both.

- **A. The server** (`internal/bcat` + `internal/stubs` + config + hosts):
  answer `list` and `data` for the `bcat-list` / `bcat-data` hosts with real
  containers built from the seed folders `switchnetctl fest build` already
  produces. Testable end to end against a patched **emulator** with no
  console and no patch risk (see §6).
- **B. The patch** (`switch/` + Prelude handoff): a `bcat.nso` ExeFS/IPS
  patch that forces the signature verify to pass, built per firmware build.
  Testable only on real hardware.

Do **A** first. It is fully verifiable here; it makes the emulator path work
immediately (emulators run no signature check — see §6.1); and it de-risks
**B** by giving the patch something real to fetch.

---

## 3. Deliverable A — the server

### 3.1 Container builder (`internal/bcat`, new file `container.go`)

A function that packs bytes into a crypto-type-0 `bcat` container:

```
BuildContainer(payload []byte) []byte
```

- Header per §2.4: magic `bcat`, version 1, crypto type **0**, hash type 0,
  secret index 0, 8 bytes padding, 16-byte IV (zero — unused for crypto 0),
  0x100-byte signature field (**zero** — the patch skips the check; do not
  invent a signature), then `payload` at 0x120.
- The payload for a `list` response is MessagePack (§3.2). The payload for a
  `data` response is the file bytes themselves — the delivery-cache files
  the seed folder already holds (`base.pack.zs`, etc.), i.e. the *decrypted*
  pack, since the container carries no per-file nisasyst layer. **Check on
  hardware** whether Splatoon 3 expects the pack still nisasyst-encrypted
  inside the BCAT container or already plain; the fest packs the seed folder
  holds are nisasyst-encrypted (`.enc`), and `GetFestDecryptionKey` is what
  the game uses to open them, so the BCAT container almost certainly carries
  the `.enc` bytes verbatim and the game decrypts them itself afterwards.
  Record which under `docs/CAPTURES/`.
- Add a test that round-trips `BuildContainer` and re-parses the header, and
  a test asserting crypto type 0 and a zero signature field (so nobody later
  "helpfully" starts signing).

Do **not** implement crypto 1/2/3 or PBKDF2. They need Nintendo's secret
strings and buy nothing once the signature is patched out.

### 3.2 List builder (`internal/bcat`, `list.go`)

Build the MessagePack `list` body from a seed folder. Reuse the existing
`Seed` type (`internal/bcat/seed.go`) as the source of truth for what files
exist. Map it to the confirmed field names:

- one `directories[]` entry per delivery-cache directory. The provided
  Splatoon 3 fest cache uses `eu-default`, `us-default`, `jp-default`, and
  `ap-default`; `fesdata` is not its region directory. The 22.5.0 module
  returns 2122-0002 when the directory requested by the game is absent;
- each `data_list[]` entry: `filename`, `size`, `data_id` (a stable small
  int per file — index is fine), `url` (see §3.3), and `digest`.
- `digest`: **open** which hash (§7). Ship it as the same 16-byte value the
  console will recompute; the safe first cut is to match whatever the
  emulator HLE compares against (Ryujinx/Citron use MD5 of the file for the
  *delivery-cache* digest — but that is the `nn::bcat` directory digest, a
  different field from the list's `digest`). Put the digest behind one
  function so §7 can be answered in one place.
- Use a MessagePack encoder. `github.com/vmihailenco/msgpack` is the usual
  Go choice; check `go.mod` first and prefer a dependency already vendored.
  If none, a hand-rolled encoder for the small fixed shape is acceptable and
  avoids a new dependency — the shape is tiny (maps of strings→scalars and
  one array). Match the repo's "stdlib-leaning" habit; ask if unsure.

### 3.3 Serving (`internal/stubs`, extend the bcat handler)

Today the three `bcat-*` hosts answer a blanket 304 (`internal/stubs`,
routed by `internal/nintendo/hosts.go`'s BCAT block — read that block's
comment before changing it; the 304 exists to avoid Splatoon 3's 2122-2403
hard error, and null-routing these hosts is explicitly forbidden).

Change the behaviour to:

- `GET /api/nx/v1/list/nx_data_<tid>`: if GRID0+ has a seed for `<tid>`,
  answer `200` with the `list` container (§3.1/§3.2). If not, keep the
  **304** — that is the correct "your cache is current / nothing new" answer
  and must remain the default, or consoles with no fest configured regress
  to the 2122-2403 failure the current comment documents.
- `GET` of a data `url` path: answer `200` with the data container wrapping
  the requested file's bytes.
- `bcat-topics-lp1` (System news): leave exactly as is (304). Out of scope.
- Preserve every existing property of this handler: `NoClientCertRequest`
  (the 2026-09-24 capture proved a CertificateRequest kills the TLS
  connection here), the `Connection: close` behaviour, and the
  `stubs.bcat_record_dir` probe hook (`internal/bcat.Probe`) — keep
  recording requests even while serving them; the transcript is still the
  only record of what a real console asks for.

**Etag / If-None-Match:** `bcat.nso` stores an `etag.bin` per title and the
list header check reads an `ETag` response header. First cut: ignore the
request's `If-None-Match` and always send the full list with a stable
`ETag`. Correct 304-on-match handling is a later refinement; getting a full
list accepted comes first. Record the console's `If-None-Match` value via
the probe so this can be done properly.

### 3.4 Where the content comes from

`switchnetctl fest build` already produces `bcat-seed/<region>-default/`
folders (`internal/bcat/seed.go`). The server reads these. Add a config key
under the existing `stubs` (or a new `bcat`) section pointing at the seed
root — mirror `stubs.bcat_record_dir`'s style, and add a `switchnetctl
check` validation that warns if the dir is set but empty. Follow
`config/config.go`'s "unknown keys rejected, validate what confuses"
conventions.

For **Splatoon 2 schedule** content: the seed files are Nintendo's real
BCAT byamls, which cannot be committed (rule 1). The operator supplies them
locally, same pattern as `SWITCHNET_BCAT_KEY_MATERIAL`. Serving is identical
once the files are in a seed folder; only the *authoring* of S2 schedules
from scratch is a separate, unsolved problem (byaml layout — out of scope
here).

---

## 4. Deliverable B — the `bcat.nso` signature patch

### 4.1 What to patch

`bcat.nso` verifies the container signature and branches on a boolean
verify result; on failure it wipes the buffer and returns 2002-4106. The
patch forces that result to "success" (the same shape as the CA/TLS verify
bypasses this project and the borntohonk repo already ship). There are two
enforcing sites (a whole-file path and a streaming path); the patch must
cover the one Splatoon 3 actually exercises, and ideally both.

**Do not** record the exact instruction offset or patch bytes in this repo
until they are produced by the proper tooling against the operator's own
build — and even then, ship them as an ExeFS/IPS patch file under `switch/`,
matching how the Splatoon 3 TLS patch is shipped, not as prose. The offset
is build-specific (22.5.0 here); a firmware update needs it regenerated.

### 4.2 How to produce it

Use the borntohonk `Switch-Ghidra-Guides` pipeline the operator already
uses (`find_patterns.py` → `generate_pattern_diffs.py` → `make_patches.py`),
or hand-locate the verify-result branch in Ghidra and emit an IPS. The build
environment here has Ghidra 12.1.2 + the Switch loader installed under
`/opt` and the analysed project under `/opt/ghproj` (see the RE session).
Producing the patch is a from-first-principles read of the two functions
named in §2.4 / the RE notes; do it against the operator's exact
`bcat_main` (they can re-send it, or re-extract with
`switchnet_bcat_extract.py`).

### 4.3 Shipping and reversibility

- Shipped as an ExeFS patch keyed by `bcat.nso`'s build id, under `switch/`
  alongside the existing patches — and in the GRID0+ Toolbox's `romfs/sd/`
  mirror from v0.5.9, so "Switch to SwitchNet" provisions it with the CA and
  the Splatoon 3 sets rather than asking for a manual copy.
- It only takes effect on the build it was made for; on any other build
  Atmosphère ignores it (build-id mismatch), so it is safe to ship a set.
- Document in `switch/README.md` that a firmware update requires
  regenerating this patch, and that without the patch the console falls back
  to the §2.4a installer path (which still works).

### 4.4 The rule-2 line

Forcing a local verify to pass is **not** forging a signature and is exactly
what CFW sigpatches already do. Producing an RSA-2048 signature that
`bcat.nso`'s baked-in public key would accept **is** forbidden and
impossible without Nintendo's private key — never attempt it, never commit a
captured signature, never commit the public-key bytes read out of the
module.

---

## 5. Order of work

1. `internal/bcat.BuildContainer` (crypto-0 container) + tests. (A, §3.1)
2. `internal/bcat` list builder from a `Seed` + tests. (A, §3.2)
3. Wire the `list`/`data` responses into the stubs bcat handler, keeping 304
   as the no-seed default and preserving the probe + TLS properties. (A,
   §3.3) Add the seed-dir config key + `switchnetctl check`. (A, §3.4)
4. Verify end to end on the **emulator** (§6.1) — this is the first point
   where a game reads a GRID0+-served fest with no homebrew.
5. Produce the `bcat.nso` patch (B, §4) and verify on **hardware** (§6.2).
6. Write up both under `docs/CAPTURES/`; delete the stale
   `TODO(capture-needed)` markers that this resolves, in the same commit;
   demote §2.4a to "fallback for un-patched consoles".

Steps 1–4 are fully doable in this environment. Steps 5–6 need the operator.

---

## 6. Verification

### 6.1 Emulator (no console, no patch)

Citron/Ryujinx HLE `bcat` does **not** verify the container signature (it is
a reimplementation of the sysmodule that just reads files). So a GRID0+
`list`/`data` server can be proven end to end against the emulator with no
patch at all: point the emulator's DNS/redirect at GRID0+ (already
supported in our Citron fork), configure a fest seed, and confirm Splatoon 3
loads the Splatfest content over the network. This is the cheapest real
proof that the server is correct, and it should gate step 4.

Caveat: because the emulator skips the signature, a green emulator run does
**not** prove the hardware patch; it only proves the server. Keep the two
claims separate.

### 6.2 Hardware (the patch)

With the patch applied: confirm Splatoon 3 downloads and renders a
GRID0+-built Splatfest with nothing in the BCAT save beforehand and no
installer run. Capture the `list`/`data` exchange (GRID0+'s own logs +
the `internal/bcat.Probe` transcript — this is a capture *against GRID0+*,
allowed; not a capture of Nintendo's traffic). Record result codes: success,
or 2002-4106 (signature still rejected → patch missed a site), or a parse
error (list shape wrong).

---

## 7. Open questions to resolve while implementing

Each is small and local; none blocks starting.

1. **List `digest` hash + whether a mismatch is fatal.** Put the digest
   computation behind one function; try the obvious (SHA-256 truncated to 16
   bytes, then MD5) and read `bcat.nso`'s list-parse path or watch the
   emulator to see which it accepts. The delivery-cache *directory* digest
   (MD5 in HLE) is a different field — don't conflate them.
2. **Data `url` host.** Does `bcat.nso` accept a `url` pointing at
   `bcat-data-lp1` only, or any host? If restricted, the `url` in the list
   must name `bcat-data-lp1` and that host serves the data; if not, it can
   point straight at a GRID0+ path. Read the `url` handling in the module
   or test both on the emulator.
3. **Data payload encryption.** Whether the container wraps the
   nisasyst-`.enc` pack bytes (most likely) or a plaintext pack (§3.1).
4. **`If-None-Match` / `etag.bin`.** For correct 304-on-unchanged; not
   needed for a first working download.
5. **`mode` values and `conditions.serial[*]`** (region/serial gating) —
   only matters if a fest must be limited; default to unconditional.

Record each answer under `docs/CAPTURES/` and delete the matching
`TODO(capture-needed)` in the same commit, per CLAUDE.md.

---

## 8. Hard constraints (from CLAUDE.md — do not violate)

- **No Nintendo key, certificate, signature, secret string, or dumped file
  in the repo** — not the public keys read out of `bcat.nso`, not a captured
  container's signature, not S2's real byaml seeds. Operator-local files
  only, `SWITCHNET_BCAT_KEY_MATERIAL`-style.
- **Never forge a signature.** The patch skips a local check; it does not
  produce a signature. (§4.4)
- **Serve crypto type 0.** No PBKDF2/passphrase path — it is unnecessary and
  would only create a reason to want the secret strings.
- **Keep the 304 default and the `NoClientCertRequest` / probe properties**
  of the current bcat hosts (§3.3); read the `hosts.go` BCAT-block comment
  before editing it.
- **Hostnames live only in `internal/nintendo/hosts.go`.** The `bcat-list` /
  `bcat-data` entries are already there; do not add host strings elsewhere.
- **Run it, don't just test it** (§6.1): a green unit test is not proof; the
  emulator reading the content is.
