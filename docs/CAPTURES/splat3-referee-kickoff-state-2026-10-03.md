# Splatoon 3: the referee kickoff verdict must arrive after @@Profile (2026-10-03)

**Settled by:** static RE of Splatoon 3's `main` (the operator's own dump, local
Ghidra project, base `0x7100000000`) plus a hotspot capture of two Switches
against GRID0+. No Nintendo traffic was decrypted.

## Symptom

A private battle loads on both consoles, P2P traffic flows both ways, and about
30 s after the consoles write `docs/@@Profile/<battle>/User/<usid>` both leave
with a communication error. Nothing reaches the server in between.

## What the game does

The gamesync referee object keeps a state at `+0x440`:

| Where | What |
| --- | --- |
| `FUN_71014ff540` | queues the `@@Profile` write and sets the state to **3** |
| `FUN_71014fe4b0` | on a `@@RefereeResult` change whose `request_id` matches: `status 0` moves **3 → 4**; `status 1` moves 5 → 6; `status` 2, 4–10 → 7 (error); `-1` moves <2 → 2. A `status 0` in any other state is ignored |
| `FUN_7103898e98` | per-frame wait: at 600 frames (10 s) resets and retries locally; at **1800 frames (30 s)** raises `GamesyncRefereeTimeoutVs2` (or its variant) |

The kickoff verdict (`status 0`) is published when the first console writes
`@@RefereeRequest`, about 15 s before `@@Profile`. At that point the console is
not in state 3 and drops it. GRID0+ never sent it again, so every console sat in
state 3 until the 30 s timeout.

Nextendo's server (regular match, 2026-09-30) re-sends the unchanged verdict
with a fresh `updateTime` every 3 s (15:32:40.34, 43.34, 46.34, ...), and also
sends one at 15:32:53.8168, right after that console's `@@Profile` committed at
15:32:53.8167. Either copy moves a waiting console to state 4.

## What GRID0+ does

`resendKickoffVerdictAfterProfile` in `internal/gamesync/referee_verdict.go`
re-publishes the battle's `@@RefereeResult` after a console writes its
`@@Profile`. The console sets state 3 when it queues that write, so a copy sent
after the write commits always lands in state 3. The 3 s session refresh also
re-sends the verdict even when it has not changed, as Nextendo does.

Not yet confirmed on hardware.
