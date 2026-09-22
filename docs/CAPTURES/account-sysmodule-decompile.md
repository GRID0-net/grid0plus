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

## What is still open

- **What the context-keyed head bytes encode.** The three big-endian u64s behave
  like per-account profile state (they differ between a login-shaped and a
  profile-shaped response for the same account) but their meaning is not
  recoverable from this binary alone. **A second account's samples arrived
  2026-08-26** ([`nextendo-login-flow-2026-08-26.md`](nextendo-login-flow-2026-08-26.md))
  and answered the half of this they could: every range the 2026-09-13 diff
  called fixed is identical there too — including bytes 22-25 reading zero,
  which is now reproduced — and the login-only pair sits at the same two
  offsets with different values. *Why* the bytes differ between a login-shaped
  and a profile-shaped response is still open, and no wire capture settles it:
  the split is server-side.
- **Whether the ten login-only bytes the real server sent (payload 56-59 and
  66-71) matter.** SwitchNet sends zeros there. Nothing in the validator reads
  them; a later check was never found.
- **Whether the eight unserved NA paths are actually called during a first
  link**, per above.
- **The `%s/avators/profiles.dat` pair** (0x1f2180 / 0x1f5730): which half reads
  and which writes is not settled. The only evidence is copy direction and a
  memset-before-fill pattern.
