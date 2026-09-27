# Splatoon 3 live GameRecord gate, 2026-09-25

This note hands off the live-debugging investigation of the hall's in-game
communication error. It records observations, not a server fix. No game dump,
certificate, key, captured credential, or private payload belongs in this file.

## Current outcome

On Splatoon 3 v11.3.0, build ID
`28c4287aee36f7499da60f3e68b54c70da382d75`, the game reaches the title
screen and plaza. Entering the hall immediately displays Splatoon's own
communication error and returns to the plaza. On this particular run there was
no preceding "multiplayer maps and modes updated" prompt. The VM still answers
the schedule calls with `OK` and `GetSaveRecord` with the empty no-cloud-save
response. The game never issues the early GameRecord `GetDocument` burst seen
in both working Nextendo captures.

The live GameRecord manager **does run**, but its first update guard returned
false. At the time of observation, its `+0x70` state was `4`, `+0x74` was `0`,
and its four work-item pointers at `+0x20`, `+0x30`, `+0x40`, and `+0x50` were
null. A watchpoint on `+0x74` did not fire during one hall attempt. Neither
VS nor Coop GameRecord fetch entry was called. This is a much narrower
client-side boundary than a missing GameRecord RPC implementation: the client
never starts that work.

The guard is not speculative. The manager update at RVA `0x328bcc0` calls
vtable slot `+0x40`; the table is at RVA `0x5d91bf0`, slot `+0x40` points to
RVA `0x328ece8`, whose complete body is:

```asm
ldr  w8, [x0, #0x70]
cmp  w8, #1
cset w0, eq
ret
```

At RVA `0x328bcf4` its result was `0` while `+0x70` was `4`. The manager
update then skips its main state-processing block. Static disassembly also
shows that when `+0x74` is zero, the update's dispatch bounds check leads to
the path that writes `4` to `+0x70`. We have **not** yet found what should set
`+0x74` or cause `+0x70` to become `1`, nor proved which server response (if
any) controls that transition. Do not treat the current empty save response as
the cause: a prior temporary real-save substitution also failed to trigger
GameRecord reads.

## Network evidence for this attempt

Around `2026-09-25 10:38:22 UTC`, VM `switchnet-npln` logs show `ValidateToken`
`OK`, `ActivateUser` `OK`, `GetViolation` `NotFound`, `SendMessage` `OK`, all
five schedule/parameter calls `OK`, then `GetSaveRecord answered with no cloud
save`. There was no GameRecord read. This is consistent with earlier plaintext
captures, but server logs do not reveal the client-side reason for the skip.

The reference captures are preserved at
`docs/CAPTURES/raw/nextendo-hall-20260923T205738Z` and
`docs/CAPTURES/raw/nextendo-second-20260923T210513Z`. Their decoded analysis
and raw `.flow` files must remain intact. Compare wire behavior and Nextendo's
Splatoon 3 server source at `/home/nico/Documents/splatfest chat dump/splatoon-3`
before changing GRID0+. Take protocol facts, not their source code or private
payloads. Previous schedule response field-shape comparisons and unsuccessful
server-side probes are summarized in the existing capture notes and commits
`798993e`, `37b8225`, and `b590e65`.

## Reproducing the live debugger

Atmosphere's standalone GDB stub was enabled on the mounted SD card by adding
`enable_standalone_gdbstub = u8!0x1` and `enable_htc = u8!0x0` under
`[atmosphere]` in
`/run/media/nico/SWITCH SD/atmosphere/config/system_settings.ini`. Original
config backup: the same path with `.pre-gdb-20260925` appended. Existing DNS
MITM settings were preserved. The console was on direct Ethernet at
`10.42.0.74`; GDB port was `22225`. Rebooting the console was required for
the setting to take effect. No third-party sysmodule was installed.

The game NSO is at `/home/nico/switchnet-re/splat3_final_exefs/main` and a
matching ELF generated with `nx2elf` is at
`/tmp/switchnet-splat3-v11.3.0-main.elf` (temporary, regenerate if absent).
Use `/home/nico/switchnet-re/tools/nx2elf/build/nx2elf` with `--export-elf`.
The available host `gdb` supports AArch64. Interactive commands:

```gdb
set pagination off
set confirm off
set architecture aarch64
target extended-remote 10.42.0.74:22225
monitor wait 0100c2500fc20000
attach <pid shown by monitor wait>
monitor get info
set $mainbase = <main module text base shown by monitor get info>
```

For the run documented here the PID was `136` (`0x88`) and `$mainbase` was
`0x3289c06000`; **these values change across launches**, so rediscover them.
All addresses below are RVAs and become runtime addresses via `$mainbase +
RVA`. The two fetch breakpoints were at `0x3283740` (VS) and `0x3283d20`
(Coop). Both were armed with commands to print registers and immediately
`continue`; neither fired during the two observed hall attempts. A temporary
breakpoint at `0x328bcc0` did fire, proving the manager update is active. One
at `0x328bcf4` recorded the false first-guard result. A hardware watchpoint
on `*(unsigned int *)(manager_object+0x74)` did not fire during the second
hall attempt. The debugger was **detached cleanly** before this note was
written; the game was not left suspended.

## Next focused work

1. Trace who writes the manager object's `+0x70` and `+0x74`, and under what
   state/event. Within the nearby manager functions, static disassembly shows
   writes to `+0x74` at RVAs `0x328c2cc`, `0x328c370`, and `0x328c444`;
   inspect their surrounding branches and callers. The `+0x70` writes in
   `0x328bcc0` include RVAs `0x328bdfc` and `0x328bff8`. A watchpoint during
   **game startup** may be more informative than one added after reaching the
   plaza, because the state may already have settled by then.
2. Distinguish whether this manager is legitimately idle until the user has
   some other prerequisite. The working Nextendo capture issues early
   GameRecord reads immediately after its second schedule batch; identify the
   equivalent client transition and what gate the fresh GRID0+ account
   fails. Do not assume GameRecord manager state alone explains the hall error
   until that relationship is demonstrated.
3. If the gate points to a server response, compare the *specific* response
   against both Nextendo captures and its server implementation before making
   a small, testable GRID0+ change. `GetSaveRecord`, `SelectVsParams`, and
   schedule timing have already been probed without a fix.

Keep all pre-existing uncommitted files (`internal/toyohr/backlog.go`,
`internal/toyohr/game_record.go`, `AGENTS.md`, `dev/`) untouched. GRID0+
`main`, origin, and the VM were at `b590e65` during this test. The VM was left
running. Do not commit any decoded private capture material.

## Static follow-up: what state 4 actually means (later on 2026-09-25)

Disassembly of the same build (ELF regenerated with `nx2elf`, based at 0, so
addresses below are RVAs) refines the reading above. The manager's vtable at
`0x5d91bf0` is a component interface: `+0x20` Initialize (`0x328b7c8`),
`+0x28` Reset (`0x328b8b0`), `+0x30` Update (`0x328bcc0`), `+0x38`
IsInitialized (`+8 != 0`), `+0x40` IsBusy (`+0x70 == 1`), `+0x50` GetState
(`+0x70`). Slots `+0x60..+0xd0` are plain field getters, not fetch entries.

- **Constructor** (`0x328b2a0`) stores the vtable at `+0`, **null at `+8`**,
  and `state = 0`.
- **Reset** writes `state = 4, kind = 0` and clears the work items. That is
  exactly the observed `+0x70 == 4, +0x74 == 0`, null items: the manager was
  reset and **never started**, not "started and failed". (Update also writes
  4 when a job finishes with a status other than OK or NOT_FOUND, so GetState
  alone cannot tell the two apart, and a hall that asks for it would plausibly
  show a communication error either way.)
- **Initialize** is the only writer of `+8`: it wraps `*x1` in the manager's
  own `ServiceClient` and does nothing if `*x1` is null.
- **Update's first guard is IsBusy**, not a readiness check: while `state == 1`
  it dispatches on `kind` (1..8) and folds the four job results at
  `+0x60..+0x6c` into state 3 or 4. The per-job handler `0x328c020` treats
  status 0 as success, status 5 (NOT_FOUND) as a normal absent document, and
  any other status as failure.
- **Starts** are virtual methods that write `state = 1` with a kind:
  `+0xd8` → kind 1 (`0x328cb10`, store at `0x328cd50`), `+0xe0` → kind 2
  (`0x328ce50`), `+0x128` → kind 7 (`0x328df90`). No direct `BL` reaches them.
  The kind-1 start returns without doing anything when `+8 == 0`
  (not initialized), when already busy, when its 32-bit id argument is 0, when
  its count is < 1, or when its record type is outside 1..4.
- Document-name formatters live beside these: `VsUserAttribute/Data`
  (`0x3283834`), `CoopUserAttribute/Data` (`0x3283e04`),
  `WeaponPowerMeasurement/%d`, `PointCardRegular/%s`, `PointCardTotal/Data`,
  `BankaraChallenges/%s`, matching the working capture's boot reads.

Ruled out on the server side while checking this: the working VsUserAttribute
carries `season_id = 16`, which is `SeasonSchedule.unk2/unk3` (GRID0+
already sends 16), not `schedule_set_id` ("10" in the capture). The working
`InitializeAttributes` carries `coop_shift.shift_id = "20260923100000"`, a
`%d%02d%02d%02d0000` stamp of the current Coop shift start.

### Live check prepared

`~/switchnet-re/gdb/grstart.py` attaches at launch (`monitor wait`), resolves
the main base from `monitor get info`, and arms non-stopping breakpoints that
log `x0..x4`, the caller (`lr` as an RVA), `[x0+8]`, `[x0+0x70]` and `[x1]` to
`~/switchnet-re/gdb/grstart.log`, at: Initialize and its `+8` store, all three
starts, the kind-1 "passed all guards" point (`0x328cd48`), Reset, and the VS
and Coop fetch entries. Run it with `gdb -q -x ~/switchnet-re/gdb/grstart.py`
**before** launching the game. One boot answers: is Initialize reached with a
live client, is any start ever called and by whom, and which guard stops it.
