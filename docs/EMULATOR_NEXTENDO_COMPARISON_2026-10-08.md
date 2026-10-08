# Nextendo and GRID0 emulator comparison — 2026-10-08

This comparison uses fixed GitHub/source snapshots. Missing commit hashes do not
prove missing behavior, and changed-file counts include branding and build files.
No Nextendo implementation was copied and no emulator release was rebuilt here.
The comparison below describes the initial snapshots; implemented follow-ups are
listed at the end.

| Repository | Compared main revision |
| --- | --- |
| NextendoNetwork/Ryujinx-Nextendo | `456c45b6438f9ae58dfcff84e4bd5fbc76807f5e` |
| GRID0-net/GRID0-ryujinx | `47bfc1e1f045` |
| NextendoNetwork/citron-nextendo | `ed943e04a8855fca1272b0b7506e2c1343116d39` |
| GRID0-net/GRID0-citron | `6ae745d7a5ce` |

Fresh isolated source checkouts were used under `/tmp/grid0-audit-*`; existing
operator checkouts were left untouched. Recent commit metadata and selected
patches came from GitHub's API, then the GRID0 source was checked for the actual
changed behavior. The Nextendo Citron checkout was recovered with a sparse
checkout after a filtered checkout failed; the relevant SSL/socket/audio/cache
and Vulkan files were available for comparison.

## Ryujinx

- **Missing UDP polling fix:** GRID0's
  `ManagedSocketPollManager.cs` marks a socket disconnected on an error when
  `!Connected || !IsBound`, without limiting that inference to stream sockets.
  UDP errors and an unconnected UDP socket do not establish a disconnected TCP
  peer. Nextendo's latest macOS Pia merge adds that stream-only condition.
  Reference: [macOS Pia fix](https://github.com/NextendoNetwork/Ryujinx-Nextendo/commit/db2ed40c735f).
- **Missing macOS rumble guard:** GRID0's SDL3 `NpadHdRumble.Create` has no
  macOS early return. The same reference merge avoids the reported IOHID open
  deadlock on macOS. This requires platform validation before release.
- **Already present:** GRID0's `KProcess.GenerateRandomEntropy` fills process
  entropy from a secure random source. Do not duplicate the October process-RNG
  fix merely because its commit hash differs.
- **Missing six-axis calibration/IC-info implementation and vertex pointer-store
  support:** GRID0 lacks the new IC-info type, calibration-store method and
  shader pointer-store descriptor. These are the latest Switch Sports fixes,
  not evidence of a Splatoon networking cause.
  Reference: [Switch Sports merge](https://github.com/NextendoNetwork/Ryujinx-Nextendo/commit/45bc2dbb5619).
- Nextendo's hard-coded service IP additions cannot be imported as GRID0 routing.
  They need comparison with GRID0's generated host/service configuration.

Ryujinx's original MIT license and Nextendo's root PolyForm Shield license are
both present. Do not blindly cherry-pick Nextendo modifications as though the
original license necessarily covers all added code.

## Citron

| Recent change | GRID0 source result |
| --- | --- |
| SSL server-certificate buffer guard (`b314969e7486`) | Missing: `DoHandshakeGetServerCert` unconditionally calls `ctx.WriteBuffer(certs_buf)`. Size-query callers can have no writable buffer; the emulator must still return chain size/count without writing an absent/undersized descriptor. |
| Do not park shutdown UDP sockets (`577130598ff8`) | Not applicable to the compared GRID0 path: its `BSD::CloseImpl` does not park sockets. Adding Nextendo's parking subsystem merely to apply this fix would introduce new behavior. |
| LRU callback bool detection (`de973a74ff08`) | Missing: GRID0 compares `std::invoke_result<...>` instead of its result type. This never recognizes a bool callback. |
| Audio queue accounting and short-read handling (`a3f4e5f4ff0b`) | Missing: GRID0 publishes the buffer before increasing queued count, lacks the zero-frame guard, and accounts requested rather than copied samples. |
| Mid-draw descriptor-overflow preservation (`abca888e5d7b`) | Missing: GRID0's overflow handler has no preserve-current-upload argument/path. |
| Non-contiguous device-memory storage buffers (`4b026b1ab3a3`) | Missing: GRID0 lacks gathered/scattered storage handling and write-back. This is a substantial renderer change, not a one-line merge. |
| Switch Sports HID/GPU support (`dbad4cd7f293`) | Separate game compatibility work; no Splatoon fix is established by its title or presence. |

Primary references: [SSL guard](https://github.com/NextendoNetwork/citron-nextendo/commit/b314969e7486),
[LRU fix](https://github.com/NextendoNetwork/citron-nextendo/commit/de973a74ff08),
[audio fix](https://github.com/NextendoNetwork/citron-nextendo/commit/a3f4e5f4ff0b),
[descriptor fix](https://github.com/NextendoNetwork/citron-nextendo/commit/abca888e5d7b),
[scattered-storage fix](https://github.com/NextendoNetwork/citron-nextendo/commit/4b026b1ab3a3).

Nextendo Citron declares GPL-3.0-or-later; GRID0's source has original GPL file
headers and its own native macOS changes. Renderer changes must be reconciled
with that branch rather than transplanted indiscriminately.

## Validation boundary and next implementation order

This is a source comparison, not a compiled emulator validation. Prioritize the
small UDP polling and SSL output-buffer defects, with focused regression tests
and actual emulator builds. Then handle LRU/audio fixes and renderer/HID changes
as separate changes with their own platform tests. No claim is made that any of
these fixes resolves the current private-match startup problem.

## Recent reference commit list

- ryujinx: `456c45b6438f` — Merge pull request #48 from NextendoNetwork/feature/game-catalog-api
- ryujinx: `db2ed40c735f` — Merge pull request #28 from Zard-Studios/fix/macos-splatoon2-pia
- ryujinx: `45bc2dbb5619` — Merge pull request #51 from Mythrax-ZS/switch-sports-fix
- ryujinx: `f4ee9cc67c7d` — HID: Implement six-axis calibration and IC info
- ryujinx: `f925f8367227` — GPU: Perform vertex pointer stores before draws
- ryujinx: `0f0731c1bdaf` — Shader: Extract constant vertex pointer stores
- ryujinx: `bd50fbf8eb3d` — About: credit Nextendo Network and the Nextendo team
- ryujinx: `53132d230fea` — Merge pull request #12 from CollectingW/arms-friends-joinable-status
- ryujinx: `f61da9afccbf` — Connect upstream Ryujinx history (Ryubing Canary 1.3.337)
- ryujinx: `698ace71dc7a` — Fix catalog launch checks and blocked account tokens
- ryujinx: `cf0daa9c3776` — Add service catalog API support
- ryujinx: `05577ab543f4` — Merge pull request #42 from NextendoNetwork/sm3dw-online-library-badges
- citron: `ed943e04a885` — Update LATEST_VERSION to 4b026b1ab [skip ci]
- citron: `4b026b1ab3a3` — buffer_cache: bind storage buffers that span non-contiguous device memory correctly
- citron: `ee364e800439` — common: return *this from RangeSet and OverlapRangeSet move assignment
- citron: `abca888e5d7b` — vulkan: keep a draw's descriptors contiguous when the payload overflows mid-draw
- citron: `c5dd7e190c75` — Update LATEST_VERSION to dbad4cd7f [skip ci]
- citron: `dbad4cd7f293` — Merge pull request #13 from Mythrax-ZS/switch-sports-fix
- citron: `de973a74ff08` — common: fix the bool-return check in LruCache::ForEachItemBelow
- citron: `a3f4e5f4ff0b` — audio_core: fix queued buffer accounting and stale samples on short reads
- citron: `b314969e7486` — ssl: only write the server certificate chain when the caller provided room
- citron: `577130598ff8` — sockets: never park a UDP socket the guest has shut down
- citron: `1e267185d138` — Add the Xenoblade Chronicles X: Definitive Edition 2.0.0 NPLN certificate patch
- citron: `74dfd6536bef` — Add Plants vs. Zombies: Battle for Neighborville online groundwork


## Implemented follow-up

- GRID0 Ryujinx `f742a32`: UDP errors no longer imply a stream hangup.
  HLE Release build passed with zero warnings/errors. Five NUnit cases exercise
  the real `ManagedSocketPollManager.Poll` and `SocketHelpers.Select` with an
  error-reporting pollable socket: two UDP and three TCP connection/binding
  combinations. All five passed. The tests do not establish macOS runtime parity.
- GRID0 Citron `240147ee8`: LRU callbacks returning true now stop iteration.
  The return-type query matches the actual lvalue callback/object invocation,
  including callbacks taking mutable references. A new Catch2 regression is
  included in the normal test target. A standalone program using the actual
  cache header failed its early-stop assertion before the fix and passed after;
  void callbacks still visit all eligible entries.
- GRID0 Citron `1c48a20ee`: SSL certificate queries preserve the required size
  and count, but write the chain only into an adequate output descriptor.
  Focused C++ checks compiled the unchanged handler and serializer bodies from
  the edited source with test IPC/backend objects. Missing, undersized, exact
  and larger buffers passed, as did the single-certificate serialization option.
  These checks are not a complete emulator build or a live guest TLS test.

The isolated Citron checkout's required source submodules were fetched. CMake
configuration then stopped because the local Boost headers/context dependency
is absent. No complete Citron build or normal Catch2 target run is claimed.
The fixes were committed and pushed to the two GRID0 repositories on main.
No tag, release asset or CI release workflow was created; installed emulators
will not gain these changes until an updated build is installed.

Remaining comparison work includes macOS rumble behavior, Citron audio accounting,
Vulkan descriptor continuity/scattered storage and Switch Sports HID/GPU support.
Those retain the validation requirements above and are not presumed Splatoon fixes.
