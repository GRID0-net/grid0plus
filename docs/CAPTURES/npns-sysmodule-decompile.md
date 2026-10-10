# npns sysmodule decompile (FW 22.5.0)

Static analysis of `npns` (title `010000000000002F`, user agent
`libcurl (nnPenne; …)`), done 2026-10-10 with Ghidra's SwitchLoader on the
operator's own firmware dump. Addresses are the module's load addresses
(`0x7100000000` base). Nothing here was decrypted from Nintendo's traffic.

## Why it matters

A console's friends sysmodule only uploads presence after npns reports state
8 (`FUN_001bf0e0` in friends waits 30 s for it). GRID0+ never got npns there,
so consoles never show "playing" and only emulators (which report through
`/grid0/emulator`) do.

## No XMPP any more

Switchbrew still describes npns as XMPP to `app-a04`. On 22.5.0 the module
contains no XMPP strings at all; it speaks **Penne**:

| Host | Paths |
| --- | --- |
| `god.hac.%.penne.srv.nintendo.net` | `/v1/penne_ids`, `/v1/accounts/%016llx%016llx`, `…/links`, `…/notification_tokens`, `/v1/immigrate` |
| `val.hac.%.penne.srv.nintendo.net` | `POST /v1/login_tickets` (`{"id","password"}`), `/v1/frontlines` |
| `frontline_fqdn` from either answer | the persistent connection |

`/v1/login_tickets` and `/v1/frontlines` are parsed by one field table
(`FUN_71000b84f0`, `FUN_71000b8b50`): `ticket`, `expires_at`, `issued_at`,
`current_time`, `frontline_fqdn`, `persistent_connection_params_simple`
{`ignore_rst`, `retry_count`, `wowl_timeout`, `wait_sec`, `rtt_max`,
`awake`/`sleep` {`idle`, `interval`, `count`, `disable`}}. Request headers:
`Authorization: Bearer`, `X-NPC`, `X-Login-Try`, `X-Fro-Result`,
`X-Tolerant`, and `X-LoginTicket-TTL` on login_tickets.
`internal/nplnrest` already answers both with these shapes.

## The frontline connection (`FUN_71000d9250`)

An **HTTP/2 POST** to `https://<frontline_fqdn>/` (CURLOPT_POST=1,
CURLOPT_HTTP_VERSION=3, low-speed limits disabled), headers:

```
Authorization: Bearer <ticket>
X-Power-State: awake | sleep
X-Hug: true | false
X-Protocol-Version: 4
X-System-Version: <firmware>
X-Network-Type: Invalid | WiFi | Ether
```

The request returns as soon as response headers arrive with status 0, 100 or
2xx; the transfer stays open. Both bodies then carry frames: **a 4-byte
little-endian length, then a FlatBuffer** (`FUN_71000e8d70`; length > 0 and
≤ the 0x3400 buffer, payload ≥ 12 bytes, verified by `FUN_71000e1cf0`).

Root table: field 0 `ubyte` union type, field 1 union body, field 2 a string
(Nextendo's frames carry `"fro"`), field 3 a `u64` Unix time — the layout
`frontlineOpeningFrame` already builds.

Union types (name table `0x7100168968`):

| # | name | # | name | # | name |
| --- | --- | --- | --- | --- | --- |
| 0 | NONE | 13 | Reset | 26 | ResendRequest |
| 1 | MessageRequest | 14 | PowerState | 27 | DAPresence |
| 2 | PublishRequest | 15 | DebugDisconnect | 28 | SubscriptionListRequest |
| 3 | KickRequest | 16 | Ping | 29 | MyAccountMessageRequest |
| 4 | PutRecord | 17 | Pong | 30 | ReloadLink |
| 5 | DeleteRecord | 18 | DebugUpdateRecord | 31 | SubscriptionUpdated |
| 6 | RootHash | 19 | DebugDeleteRecord | 32 | Hi |
| 7 | LeafHash | 20 | Idle | 33 | Onset |
| 8 | SyncComplete | 21 | PutRequest | 34 | PresenceNotifyRequest |
| 9 | Ack | 22 | DeleteRequest | 35 | DenySubscribe |
| 10 | HandoverRequest | 23 | SubscribeTopic | 36 | TopicRead |
| 11 | HandoverResponse | 24 | Nack | 37 | CrewNotifyRequest |
| 12 | HandoverResult | 25 | SubscriptionList | | |

## Session states (`*(int*)(session+8)`)

`2 → 3 → 4 → 5` (connect) `→ 0x10 → 6 → 0xe` (TLS/HTTP setup) `→ 7`
(request sent, `FUN_71000d9250`) `→ 8` (steady, `FUN_71000dc9e0`).

In state 7 (`FUN_71000d9730`, 70 s timeout) the first frame must be
HandoverResult (or Reset):

- **body field 0 true** → `FUN_71000da010` → state 8 directly, but only after
  asserting the local digital twin matches an expected root
  (`FUN_71000b1120`); a failed assert aborts the process.
- **body field 0 absent/false** — what Nextendo, and therefore GRID0+, sends —
  → `FUN_71000da1a0`: the client sends its own twin hash (`FUN_71000d4010`) on
  the request body and waits (`FUN_71000da2f0`) for RootHash (6) or LeafHash
  (7), then PutRecord/DeleteRecord, then SyncComplete, before state 8.

**This is why npns never reaches state 8 on GRID0+:** `internal/nplnrest`
sends the empty HandoverResult and then discards the request body, so the
twin sync never completes. nginx logs show `nnPenne` opening `POST /` on the
frontline and getting the 99-byte opening frame.

In state 8 the server may send PutRecord, DeleteRecord, Ack, Reset, Ping,
Pong, Idle, Nack, SubscriptionList, SubscriptionUpdated and Hi; anything else
is "Unexpected command … in steady state".

## Still open

- the client's twin-hash message (`FUN_71000d4010`) and the exact
  RootHash/LeafHash/SyncComplete bodies the sync expects (`FUN_71000da2f0` and
  the steps at `0x710016b460`/`b468`);
- what the client sends periodically in state 8 (Ping cadence from
  `awake.interval`, and whether Pong is required);
- error codes 0xcd–0x24e are named in `FUN_71000d7b30`.

## Implemented (2026-10-10, `internal/nplnrest`)

- **Sync:** a console RootHash is echoed back (no server-side records, so the
  twins agree), which takes npns to state 8. Pings get a Pong carrying their
  first field. Nothing is sent unprompted.
- **Presence mode:** the first record after the sync is the control
  notification `presence:nxbaas` on topic `penne` (FUN_71000d64c0 /
  FUN_71000d6070), keeping the friends sysmodule on the BAAS presence path
  (friends FUN_001bf3c0) rather than handing presence to npns (FUN_001bf220).
- **Push path:** `god.hac` `/v1/accounts/<Uid>/notification_tokens` records
  `token -> (penne_id, Uid)`; BAAS `PUT /1.0.0/push_channels` records
  `user -> token`; `/v1/login_tickets` records `ticket -> penne_id`; the
  frontline's `Bearer` ticket names its penne id.
- **Notifications:** PutRecord { records: [ { kind 1, Notification { id,
  kind 1, { topic "0x010000000000000e", Uid, target "" }, payload, expiry } } ] }.
  One in flight per connection until npns acks (it aborts on a second).
  Payloads are the friends sysmodule's JSON: `presence_updated` with the
  friend-list presence object under `value`, `friend_request_received` with
  the inbox item, `friend_request_authorized`, `friend_deleted`.

Still unconfirmed on hardware: whether npns reaches state 8 on the echo,
which field of a Ping is its id, and whether the Ack that releases the next
record is the type-9 frame this server waits for. The frontline logs every
frame in both directions to settle each.
