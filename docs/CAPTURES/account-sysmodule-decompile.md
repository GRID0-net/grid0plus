# The `account` sysmodule, decompiled (2026-09-22)

Static analysis, not a packet capture, but it belongs here for the same reason:
it is what settled several of `internal/baas`'s guesses, and it is where the
next person should look before changing `nxAccountBlob` again.

**What the input was.** The operator's own console's `account` sysmodule (title
`010000000000001E`), from a firmware 22.5.0 package, decrypted with the
operator's own `prod.keys` and analyzed headlessly with Ghidra. Nothing derived
from it is committed here — no binary, no decrypted segment, no Nintendo-issued
material. What is recorded below is findings about behaviour and wire shape,
which is the same bar the rest of `docs/CAPTURES/` holds itself to.

Addresses are Ghidra's, which map to this repo's `FUN_71000XXXXX` naming as
`address + 0x70FFF00000`.

## What it settled

**The blob's consumer is real and its check is narrow.** `FUN_71000f0960`
(import address `0x1f0960`), reached from the federation success path, requires
the base64 text to start with `01606150`, requires the remainder to decode to
*exactly* 168 bytes, and then requires the first 16 of those bytes not to be all
zero. It examines nothing else. Both of its callers — `0x1fd120` and
`0x1fd4c0`, the NAS linkage and reauthentication state machines — `return
0x2487c`, i.e. **2124-0292**, when it fails.

That range is the correction this pass forced on the code: `accounts.go` said
"roughly the first 24 decoded bytes" and the test asserted blob bytes 6-29. Both
were wider than the check, which means the test would have passed on a value the
console refuses. Fixed in `e9a86f7` to payload 0-15 / blob bytes 6-21.

**The 2026-09-21 regression is explained.** `FUN_71000f0ee0` builds a 265-byte
*internal record*: a 33-byte naID at `+0x00`, the ASCII `01606150` at `+0x21`,
and base64 of the 168-byte payload at `+0x29`. So the ASCII string is the
record's copy of the header's base64 text, while the *wire* value's offset 0 is
that same text decoded — the 6 bytes `d3 5e b4 eb 5e 74`. Both readings were
correct about their own structure, which is exactly why moving the ASCII to
offset 0 broke the wire value.

**The payload's skeleton.** Payload 0-23 is three big-endian u64s rendered from
the account's cached profile record; 24-39 is two zero quads the builder writes
itself; 40-167 is sixteen u64s copied verbatim from that record. The
`01 00 01 01` marker at blob offsets 46-49 is the low half of the first copied
u64, and the capture's 96-byte zero tail is the rest of the copied region being
zero. `nxAccountBlob` reproduces that skeleton and nothing more.

**A second error code in the same module.** The profile cache lookup
(`0x1f32c0`) returns `0xc87c` when no slot matches the calling Uid. Switch
encodes errors as `(description << 9) | module`, so `0xc87c` and `0x2487c` are
the same module (124) with descriptions 100 and 292: **2124-0100** for the
profile-cache miss, **2124-0292** for the blob check.

**The profile cache itself.** Eight slots of `0x300` bytes at object `+0x330`,
keyed by a 16-byte `nn::account::Uid` at each slot's `+0x00`, with a mutex at
`+0x120`. Per slot: the three u64s the blob renders big-endian at `+0x10`,
`+0x18`, `+0x20`; the naID string at `+0x28`; sixteen u64s at `+0x48..+0xC8`
that the blob copies verbatim; an avatar-format flag at `+0xC8`; and a UTF-16
nickname buffer at `+0x408`.

**`$.isNnLinked` is read from a server response.** `0x1c44a0` is a JSONPath
field table over the Nintendo Account profile response, alongside
`$.analyticsFor*`, `$.nickname`, `$.screenName`, `$.isChild` and `$.links.*`. It
is not local state — so if the console's post-federation failure is a linkage
check, whether SwitchNet serves that field in that response is a live question.

## What it newly suggests

**Eight `accounts.nintendo.com` paths have no SwitchNet route.** The sysmodule
can construct 42 endpoints; every BAAS one is already served, but on the
Nintendo Account host only `authorize`, `api/token`, `2.0.0/users/me` and
`qrcode_param` are. Unserved, and all first-link surface:

| path | what it is |
| --- | --- |
| `%s/profile` | the NA profile fetch |
| `%s/connect/1.0.0/api/term_agreement_token` | terms-of-service agreement |
| `%s/connect/1.0.0/term_confirm` | terms confirmation |
| `%s/connect/1.0.0/api/transfer_na_tokens` (+ `/%lld`, `_inherit`) | Nintendo Account transfer |
| `%s/analytics/device/opt_in`, `%s/app/analytics/device/opt_in` | analytics permission |

The terms pair lines up with the `SetCntPlayerNALicense` scene in `qlaunch`'s
add-user wizard, and the `transfer_na_tokens*` trio is the "import an account
whose original console is gone" path. That makes "the console walks terms →
analytics → profile after federation and gets 404s for all of them" a
candidate for the post-federation "account couldn't be linked" dialog.

**It is only a candidate.** String presence proves a URL is constructible, not
that it is called. The cheap discriminator is the nginx access log during one
real link attempt (see `TODO.md` §4.3 and `docs/QUESTIONS.md`) — no MITM, no
console-side change.

**The `acc:*` front end is in `ns`, not in `account`.** The account sysmodule
contains no `nn::account::IAccountService*` type names and no `acc:*` service
strings at all. `ns` (`010000000000001F`) contains `acc:u0`/`acc:u1`/`acc:su`/
`acc:e`, the server-side instantiation
`HipcSimpleAllInOneServerManager<…AccountPolicyServerOption>`, and the policy
implementation classes `AccountPolicyServiceImpl` and
`BaasManagerForApplicationImpl`. So whoever continues IPC work should analyze
`ns` for the service surface and `account` for the data and network layers.
This comes from framework type names and service-name literals, not from
reading the dispatch table.

**Looking for that dispatch table failed, and the failure is informative.**
`ns` was imported and analyzed, and a scan for the expected
`{ u32 id; u32 pad; Handler handler; }` shape found no run of four consecutive
entries anywhere in the module — none under that layout, nor under four other
candidate layouts (16- and 24-byte strides, id above or below the pointer,
zeroed or non-zeroed padding). The template the module instantiates is
`HipcSimpleAllInOneServerManager<64, 3, …AccountPolicyServerOption…>`, and
"simple all-in-one" plus a compile-time option list is consistent with the
command table being a `constexpr` list the compiler folded into code rather
than a data table. So the command ids are **not** recoverable by table scan,
and naming individual handlers would need the client proxies' ids correlated
against argument shapes — a much deeper analysis than this pass, and one that
would buy little here, since SwitchNet implements the HTTP protocol, not the
IPC surface.

**The OAuth procedures are in `playerSelect`.** `0100000000001007` carries
`NintendoAccountLinkageProcedure`, `NintendoAccountLinkageStateUpdateProcedure`,
`NintendoAccountReauthenticationProcedure`,
`NintendoAccountApplicationAuthorizationProcedure`,
`ExternalNetworkServiceAccountIntroducingProcedure`,
`UserRegistrationProcedure` and the two `DeviceMigration*Procedure` classes —
the concrete wizards behind the add-user flow, next to `detail::OAuthProcedureBase`.
`qlaunch` holds the scene table, the Cruiser web applet the login page, and
`migration` the console-transfer half. This answers the "which component runs
the add-user / import flow" question in `nintendo-account-link.md`.

## The linkage procedure, step by step

`an_nas_oauth_linkage_stateMachine` (0x1fd120) is the wizard the add-user flow
runs, and its order is *not* the order the wire suggests:

| # | Call | What it does | Failure |
| --- | --- | --- | --- |
| 1a | `FUN_001d8910(…, 0x81333c548b2e876d)` | a dauth application token: `POST /v5/application_auth_token`, or `/v8/challenge` + `/v8/device_auth_tokens` when the cached one is dead | its return |
| 1b | `FUN_00204260` → `FUN_001ccfe0` | **`POST /1.0.0/login`**, form `id&password&appAuthNToken&naCountry&isPersistent` | its return |
| 1c | `FUN_001fd890` | **`GET /2.0.0/users/me`**, parsed by `parse_na_profile_permissions` — unconditional on 1b succeeding | its return |
| 2 | `fs_nas_dat_a` (0x1c5a40) | ensures `%s/nas/%016llx.dat` exists; returns 1 — which the machine turns into `0x2467c` — exactly when the file is **already present at size 0x80**; a wrong size is rewritten, and "not found" returns 0 | 0x2467c |
| 3 | `FUN_001ca070` → `FUN_001cd6d0` | **`POST /1.0.0/federation`**, form `id&password&idp=nintendoAccount&naCountry&idToken`; fills the record `{naID@0x00, nxAccount@0x21, url@0x122}` from the response, but only if the response names the same user id step 1 produced | its return, two server codes remapped to `0x2447c` |
| 4 | `an_nxAccount_validateRecord` | the three checks above | 0x2487c = 2124-0292 |
| 5 | `FUN_001f06a0` | POSTs the record's third string if it is non-empty, 128 KB cap | its return |

Two consequences for reading a real console's nginx log:

- **A login with no `GET /2.0.0/users/me` after it means step 1b's response was
  rejected** — 1c is unconditional, so its absence is not "the console decided
  not to". A login whose `users/me` *did* arrive, but which is followed by no
  `federation`, failed at step 2 instead — a local file op, invisible to the
  server.
- **A failure inside a console sysmodule leaves the same signature as one of
  these.** The working 2026-08-26 capture's call immediately after a login is
  `PUT /1.0.0/push_channels/<user>/<device>`, whose body carries an **npns**
  device token — so when push-channel registration never reaches the server,
  the cause can be the npns/penne bootstrap and not BAAS at all. Every host in
  that chain is served by SwitchNet's generic stubs (`consumer.lp1.npns…`,
  `god.hac.lp1.penne…`) or by `nplnrest`'s unhandled-path fallback, which
  answers a bare `{}` — the two-byte 200 seen on
  `POST /v1/accounts/<id>/notification_tokens` in the 2026-09-22 hardware log.
  `internal/stubs` logs every request it answers ("stub request"), so the
  console's npns/penne paths are readable from that service's log.

## Where the head bytes come from (settled 2026-09-22, same pass)

They are **not derived by the console at all** — they are round-tripped
server data, and the round trip is visible end to end in three functions:

| Function | Role |
| --- | --- |
| `an_profileCache_updateProfile_a` / `_b` (0x1f1640 / 0x1f1c40) | writes the selected slot's `+0x10`, `+0x18`, `+0x20` from `param_3[0]`, `param_3[1]`, `param_3[2]`, and `+0x48`..`+0xC0` from `param_4[0..15]` — together with the avatar it downloaded and the UTF-16 nickname it was handed |
| `an_profileCache_readSlotFields` (0x1f32c0) | copies those same two regions back out |
| `an_nxAccount_buildRecord` (0x1f0ee0) | renders the first three as **big-endian** u64s into payload 0-23 and copies the other sixteen verbatim into payload 40-167 |

The slot is selected by a 16-byte key (`param_2`), and `updateProfile` returns
`0xc87c` if no slot matches it — so the cache has to have been created for that
account first. **Net effect: the head is whatever a response last told the
console to cache.** That is the mechanism behind the per-response difference in
the table below, and it also rules out the reading that worried this file most:
a console cannot be checking the head against a derivation of its own, because
it never holds anything but a copy of the server's own bytes. The server
chooses them; the console stores them and echoes them in the next blob it
builds.

## What the validator actually constrains

`an_nxAccount_validateRecord` (0x1f0960), re-derived from its own decompile
rather than from the caller's error path:

1. the 8 bytes at `record+0x21` equal ASCII `01606150` — which is not a
   separate marker: it is what base64 of the `d3 5e b4 eb 5e 74` header *is*,
   so any correct encoding of that header produces it for free;
2. `an_base64_decode` over `record+0x29` (the 224 characters after those 8)
   returns success with a length-out of exactly `0xa8` — 168 bytes;
3. a byte-permuting non-zero test over the **first 16 payload bytes** (blob
  6-21). Both words it tests are byte-swapped, and byte-swapping preserves
   which bytes are zero, so this is exactly "not all zero".

and nothing else — the remaining 152 payload bytes are read by nothing in
either caller. `nxAccountBlob` satisfies all three, including with the head it
derives from `naID`. So a `2124-0292` from this gate means the field was absent,
empty, mis-prefixed or the wrong length at the moment the record was built —
not that its *content* was wrong.

## What is still open

- **What the context-keyed head bytes encode.** The three big-endian u64s are
  server-supplied and console-cached (above), so their *meaning* is a
  server-side choice, not something this binary or any capture can decode. A
  second account's samples arrived 2026-08-26
  ([`nextendo-login-flow-2026-08-26.md`](nextendo-login-flow-2026-08-26.md))
  and confirmed every range the 2026-09-13 diff called fixed, including bytes
  22-25 reading zero. The narrowest remaining question is therefore not "what
  does SwitchNet lose by inventing them" — nothing reads them — but whether a
  *future* console build compares a cached value against a refreshed one.
- **Whether the ten login-only bytes the real server sent (payload 56-59 and
  66-71) matter.** SwitchNet sends zeros there. Nothing in the validator reads
  them; a later check was never found. They land in the same copied region as
  the `01 00 01 01` at payload 40-43, which reads as four booleans
  (`personalAnalytics` / `personalNotification` / `friendRequestReception` and
  a zero, in some order) — the region looks like the *permissions* block of a
  user object, which would make the ten login-only bytes its
  `*UpdatedAt` timestamps.
- **Whether the eight unserved NA paths are actually called during a first
  link**, per above.
- **The `%s/avators/profiles.dat` pair** (0x1f2180 / 0x1f5730): which half reads
  and which writes is not settled. The only evidence is copy direction and a
  memset-before-fill pattern.
