# NPLN stream liveness measured from successful hall sessions

Two private, successful Nextendo sessions captured on 2026-09-23 are preserved
under `raw/nextendo-hall-20260923T205738Z` and
`raw/nextendo-second-20260923T210513Z`. This note records protocol facts,
without copying response bodies, credentials or player data into source.

| RPC | Successful capture response | SwitchNet before this change |
| --- | --- | --- |
| `LobbyMessaging/RecvMessage` | Keepalive at about 20, 40 and 60 seconds; empty `KeepAlive` fields | 30-second tick |
| `SubscribeMaintenanceSchedules` | Initial keepalive and another every 20 seconds | 30-second tick |
| `Friends/SubscribeFriendUsers` | Initial snapshot advertises `keep_alive_interval: 50s`; unchanged-list pulses at 20, 40 and 60 seconds carry only that interval | Advertised 30s and resent the full friend snapshot every 30s |
| `PresenceService/SubscribePresences` | Opening heartbeat advertises `interval: 30s`, followed by a presence page and `enumeration_done`; heartbeat-only pulses at 20, 40 and 60 seconds | No opening heartbeat, then a 30-second tick |

The first capture ends earlier, but independently shows the same 20-second
pulse on each stream. The second capture covers all three pulses. The capture
timestamps come from `stream-events.jsonl` and the field shapes from the
private decoded gRPC messages in each capture's `analysis/` directory.

In a SwitchNet VM attempt on 2026-09-24, the game completed its boot RPCs,
then called `Auth/ValidateToken` about 41 seconds after opening its streams.
Around 44 seconds, the console canceled all five long-lived NPLN streams
within the same instant, showed a communication error on hall entry, and
reopened a friend stream that nginx recorded as HTTP 400 only after the cancel
burst. That sequence makes missing or incorrect stream liveness a concrete
lead, but the capture alone does not prove which stream triggered the client.

SwitchNet now uses a 20-second pulse on the four measured streams. The friend
stream checks for a changed snapshot at each pulse and sends a full update
only if it changed; otherwise it sends the captured interval-only keepalive.
The presence stream announces its heartbeat before its initial page. Its
advertised 30-second interval and the friend's advertised 50-second interval
remain distinct from the observed 20-second send cadence. A new hardware
attempt is required to see whether the hall error disappears.
