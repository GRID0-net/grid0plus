# NPLN: TrackMatchmakingTicket must end with OK after SUCCEEDED (2026-10-03)

**Settled by:** the Nextendo regular-match capture of 2026-09-30
(`raw/nextendo-ryujinx-match-20260930`, mitm flows, GRID0+-side tooling only).

In all six matchmaking rounds the server ended the `TrackMatchmakingTicket`
stream with `grpc-status: 0`, and the console called gamesync `IssueToken`
30–50 ms later (stream end → IssueToken: 660.93→660.97, 861.54→861.58,
906.66→906.70, 949.23→949.27, 345.20→345.25, 670.08→670.10). The console
waits for that close; no other request happens in between.

GRID0+ used to keep the stream open until the console hung up, from an earlier
reading of a 2026-08-13 Nextendo note about racing the console's transport. On
hardware (2026-10-03, two Switches, turf war sized to 2) both tickets reached
SUCCEEDED and the consoles stayed on the search screen with the cancel button
still shown, never calling IssueToken.

`TrackMatchmakingTicket` now ends the stream with OK `succeededCloseAfter`
(2 s, the shortest gap in the capture) after sending SUCCEEDED.
