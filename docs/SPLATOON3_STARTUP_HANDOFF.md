# Splatoon 3 startup readiness and minus-button timing handoff

Updated 2026-10-16. Read this first, then the full evidence log:
[peer-watch investigation](CAPTURES/splat3-peer-watch-ordering-2026-10-14.md).

## Current outcome

A three-player battle with the real Switch hosting on a hotspot and two PC
emulators entered successfully after installing the new readiness IPS on the
Switch. The server and emulator binaries were unchanged during that test.
This validates that configuration once; repeated reliability, Salmon Run and
private-room behavior with this new gate still require hardware testing.

The user wanted to shorten the delay between the Switch's cancel/minus
prompt disappearing and the emulators' prompts disappearing. No additional
timing patch has been justified or installed; see the static and capture
conclusion below. The operator chose not to pursue it for now.

## Confirmed defect and correction

Original `WaitFullmember` compares Pia connected stations with full size (8
Versus, 4 Coop). The old small-match patch bypassed equality and used a lobby
`+0x490` threshold that remained zero on native three-player runs. Its zero
fallback let the host pass after 61 updates with only itself connected.
Ryujinx could also pass with an unready Pia property (`w0 = 0xffffffff`).

The independent `docs/__pgn/All/__pus` peer group already contains the complete
assigned roster. Attempt 11 measured three accepted members and completed
snapshot flags on both devices before the old gate passed with Switch=1 and
Ryujinx=unready. Attempt 12 independently confirmed the ownership chain on
both devices during a successful same-network three-player start.

The replacement gate:

- acquires the current session under the manager's mutex;
- locks native API/group weak owners and holds their shared references;
- requires the `All` group, own-member and snapshot-complete flags;
- requires a positive connected count equal to the assigned count, bounded by
  the original mode's full size;
- restores the original return value, LR, SP and callee-saved registers;
- retains the existing 61-update stability counter and timeout path.

Do not replace the assigned count with occupied Pia slots or `__us` documents:
both grow as peers connect. The secondary `__us` group was inactive in tested
starts and its native count returned -1. Those earlier candidates were rejected.

## Binary and ownership landmarks

Build: Splatoon 3 11.3.0, build ID
`28C4287AEE36F7499DA60F3E68B54C70DA382D75`.
All addresses below are RVAs, not absolute process addresses.

- WaitFullmember comparison `0x38a420c`, trampoline hook `0x38a4210`;
  wait destination `0x38a4254`, enough destination `0x38a4214`.
- Pia count function `0x15173d0`, property key `0xc056fc0f`.
- `__pus` update callback `0x424ae0`, group pointer at lambda+0x10.
- Group name libc++ string +0x1a8; member map count +0xb8;
  own-member flag +0x1fa; completed snapshot flag +0x1fb.
- Manager root `*(main+0x61e18a0)`, manager at root+0x80;
  session count +0x12a8, array +0x12b0, selected index +0x12b8;
  entries are 0x150 bytes, manager mutex +0x2d0.
- Entry's leading word must be nonzero. Entry+8 points to a box; `*box`
  points to the API weak handle; handle[0] is API and handle[1] its owner.
  API+0x10 points to impl; impl+0x30 is group, impl+0x38 its weak owner.
- Native group getter `0x4028dc` uses API in x0 and output shared pair in x8.
  Weak lock `0x4e8c190`, mutex lock/unlock `0x4e8bf30/0x4e8bf40`.
  Shared owners use a 64-bit count at +8; -1 is expired. Do not merely read
  cached callback pointers from a shipping patch without retaining ownership.

## Sources, tests and releases

Toolbox repo: `/home/nico/cfw-GRID0plus`, remote
`GRID0-net/GRID0plus-cfw`. Commit `3709200`, release **0.8.10 published**.

- `toolbox/tools/make_s3_peer_group_gate.py`: readable ARM64 and fixed caves.
- `toolbox/tools/s3_peer_group_wait_records.json`: 49 readiness records.
- `toolbox/tools/test_s3_peer_group_gate.py`: 284 Unicorn cases, including
  expired owners and last-owner destruction after session replacement.
- `toolbox/tools/make_s3_smallmatch.py`: combines readiness with retained fixes;
  pass the operator's flat image to check every original patch span.
- Existing rematch execution test still passes all 16 cases.

Ryujinx repo `/home/nico/GRID0-ryujinx`, commit `1b0d77055`, release
**1.4.9 published**, all platform jobs and tests succeeded, run `38779593267`.
Embedded IPS lives in `src/Ryujinx.HLE/HOS/Patches/exefs_patches/`.

Citron repo `/home/nico/GRID0-citron`, readiness commit `8194d1295`.
Windows/macOS **1.4.1 built**, not yet published as a complete release.
Their downloaded executables contain the exact tested IPS. Linux run
`38779597892` failed downloading renderutil after upstream TLS resets.
Build-only fix `aa9d0851a` pins a byte-identical Debian mirror archive by
SHA256. Repaired Linux-only run **38813894582** was in progress at handoff,
with `publish=false`. Finish and verify it, combine all three artifacts,
then publish **1.4.1**. Do not announce completion until assets exist.

Successful Windows/macOS artifacts are retained locally:
`/tmp/grid0-citron-1.4.1-release/{windows,macos}/`.
If missing, download those named artifacts from original run `38779597892`.

Serialization caveat: the hardware-tested/emulator IPS hash is
`a855cfec093a8c8f0cb441ceb46da44f8afbb07d293fba102f14ee2415310fa6`.
The regenerated Toolbox IPS hash is
`80c0c81cabf0109dc1a0253518d252431a4e55873d7960dc2c0b2d803f305be2`.
They contain the same 61 records in a different order. Parsed address-to-byte
writes compare equal. The published Toolbox NRO contains two exact copies of
its regenerated IPS. Whole-file hash inequality does not indicate a code drift.

## Minus-button findings and next measurement

The exact hold-minus binding is `L_HoldCancel_00`, constructed at `0x41c17d0`.
Its widget is stored at object+0x258 and uses animations `MeterUp_Minus` and
`MeterDecide_Minus`. Actual visibility setter `0x41c1650` receives desired
visibility in w1 bit zero, compares old value at object+0x28d, and does nothing
if unchanged. Lobby wrapper `0x38ea888` receives desired visibility in w0 and
calls the widget under its UI mutex. It has no independent countdown.

Lobby callbacks `0x38f4aac` show minus on state entry; `0x38f5114`,
`0x38f57d8`, `0x38f6cd8` hide it on state entry. Constructor `0x38f3c9c`
binds callbacks using ADR instructions. Update `0x38f57d8` can later move the
network controller at object+0xcc8 into state 0x17. Thus UI disappearance
and network readiness are distinct events. Exact active callback/state during
the operator's observed gap still needs measurement; do not invent its meaning.

### Static and capture conclusion (2026-10-16, no new test)

The lobby leaves the minus-shown state when `FUN_71038fc10c` returns 2.
That function reads `+0xeac` on the `Cmn_VersusMatch_01` layout object,
not a network state. The layout's `+0xeac` values are written by
animation/part-finished callbacks (`0x437120c`: the part at `+0xe18`
finishing sets 2; `FUN_7104370e3c` sets 1/3/7/8/12/13). So the hide is
paced by UI animations, which start only after that device's own
readiness gate has passed. That gate needs 61 stable updates.

Attempt 13's server capture rules out a late emulator link:

- TURN allocations for all three players at 17:34:00.64–00.92 UTC.
- Switch↔emulator relay permissions, channels and the first data at
  17:34:01.75–01.86, both directions, within about 110 ms.
- Emulator↔emulator traffic never used the relay (same PC, direct).
- All three referee profiles arrived at 17:34:10.33–10.34, within 11 ms.

Every link was up about 8 s before a synchronised battle start, so the
prompt gap is not network. The remaining explanation is frame-paced UI
and the 61-update gate running at the emulators' lower or uneven frame
rate. This is inferred, not measured on the emulators. It is cosmetic,
because the battle starts together on all devices. The operator chose
not to patch it: shortening the 61-update counter weakens the readiness
fix, and skipping the layout animations means patching unverified UI
code. Revisit only with a probe measurement showing otherwise.

Read-only probe `scripts/gdb/splat3_readiness.py` now observes wrapper requests
and actual changed visibility, their callers, Pia count and owned-group state.
Both new hooks are bounded to 64 hits and auto-continue. Synthetic checks
verified argument registers, transition logging, unchanged suppression and bound.

For the next test, attach Ryujinx on `127.0.0.1:55555` only after its game
starts, and start a fresh bounded VM capture. Switch on hotspot cannot be
reached through its former LAN GDB address, so do not pretend to attach it.
Keep all players out of matchmaking until the capture/probe is confirmed.
Then force-start three players with Switch hosting and ask when each minus
prompt disappears. Stop capture and detach GDB afterward.

Example (choose a new log name; preserve previous files):

```sh
GRID0_GDB_KIND=ryujinx GRID0_GDB_TARGET=127.0.0.1:55555 \
GRID0_GDB_LOG=/absolute/private/path/new-minus-test.jsonl \
gdb -q -nx -x scripts/gdb/splat3_readiness.py
```

Cleanup: Ctrl+C, `delete breakpoints`, `detach`, `quit`.
Do not leave a client paused. No new game patch is needed for this observation.

## Capture evidence and preservation

All private evidence is in
`docs/CAPTURES/raw/grid0-three-player-20261015/` (gitignored).
Preserve raw files, including all prior Nextendo captures elsewhere under raw.
Never commit dumped executables, credentials, certificates or private captures.

- Attempt 11: old gate passed with complete roster=3 but connected=1/unready;
  emulator freeze makes battle outcome inconclusive.
- Attempt 12: successful original-patch same-network control; native owner
  path matches callback group, connected=3 on both probes.
- Attempt 13: new Switch patch, Switch hosting on hotspot, all three entered;
  three streams attached 17:33:54.230-.298 UTC; referee profiles reached
  1/3, 2/3, 3/3 at 17:34:10.327/.332/.338 UTC.
- Attempt 13 TURN Allocate requests arrived 17:34:00.697/.724/.920 UTC;
  VM replies followed in under 1 ms. First captured Send Indication was
  17:34:01.755. Prompt server replies do not explain the entire client gap.
  These are network timestamps, not measured UI-hide timestamps.
- Original SD IPS backups: `sd-before-peer-group-20261015T172918Z/`.
  Installed Switch candidate remains the tested hash above.
- `analysis-private/minus-*.c`, `pus-*.c` retain static findings.
  Discard the invalid exploratory function at interior RVA 0x41c1c00;
  only verified true entries should be decompiled as independent functions.

Ghidra project `/home/nico/switchnet-re/ghidra_projects/splat3_main` is opened
read-only with `-noanalysis`; original ELF is
`/home/nico/switchnet-re/splat3_final_exefs/main.elf`.
No temporary project clone is necessary. Do not run concurrent sessions on the
same project. Static xrefs miss some direct calls and ADR-built tables: validate
with raw ARM64 disassembly/branch scanning before concluding no callers exist.

### Release completion process

A one-shot local finalizer `/tmp/grid0-finish-citron-release.py` is running
while Linux job 38813894582 builds. It polls that job, downloads the Linux
artifact only after success, verifies the tested IPS inside all three actual
executables (extracting the Linux AppImage), then creates release v1.4.1 from
commit aa9d0851a with all three assets. It refuses to overwrite an existing
release. It is not a recurring automation and does not restart any games.

Outcome: run 38813894582 failed on 2026-10-15 at 22:16 UTC. That run did not
fail on renderutil: it failed fetching `xcb-util-keysyms` from
`xcb.freedesktop.org`, which was not answering at all. The finalizer exited
with `failed` and published nothing. The operator chose to wait for the host to
return and re-run, rather than mirror every xcb package. If it stays down,
`xorg.freedesktop.org/archive/individual/xcb/` carries libxcb 1.16,
xcb-util-image 0.4.1 and xcb-util-keysyms 0.4.1, and Debian's keysyms
orig.tar.xz is byte-identical to it
(`7c260a5294412aed429df1da2f8afd3bd07b7cba3fec772fba15a613a6d5c638`).
After a successful re-run, update the run ID in the finalizer and run it again.

Done 2026-10-16: xcb.freedesktop.org returned, the failed Linux job of
38813894582 was re-run and succeeded. The finalizer was gone from `/tmp`, so
its steps were done by hand: all six private-server IPS files were found
byte-for-byte inside the Linux, Windows and macOS executables, and
[v1.4.1](https://github.com/GRID0-net/GRID0-citron/releases/tag/v1.4.1) was
published from aa9d0851a with the three assets.

Check `/tmp/grid0-citron-1.4.1-release/publication-status.json` before doing
manual publication or starting another finalizer. `published` includes the
verified URL/asset names; `failed` includes the blocking error. If the process
or `/tmp` files are gone, reconstruct from the run IDs above. Do not assume
release completion merely because this process was started.

This handoff and the full evidence log/probes were committed in grid0plus
commit 27cf618 and pushed to main. Private captures were not staged.
