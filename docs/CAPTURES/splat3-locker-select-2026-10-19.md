# Locker `SelectDocuments` — why an empty answer locks the lobby

| | |
| --- | --- |
| Host | NPLN gRPC, `nn.npln.toyohr.v1.Locker/SelectDocuments` |
| System version | 22.5.0 |
| Title | Splatoon 3 `0100c2500fc20000`, 11.3.0 (`main` build 28C4287A…) |
| Captured | 2026-10-19 |
| Tool | static RE of `main` (Ghidra, base `0x7100000000`) + live reads with the Atmosphère gdbstub on a real console |

## What the console was doing

Standing in the lobby on grid0 while the big screen stayed dark and the
locker room door stayed shut, for every player. grid0 had no locker posts,
so it answered the lobby's request (type `Normal3`, `UIDs` = eleven other
players, not the caller; `UIDsInOrder` empty) with no documents.

## What was read

- **The big screen** (`spl::LobbyScreen`) sat in `State::LockedWaitDownload`
  (`0x71022e0790`). It leaves either when `LobbyVersus.IsWatchedFirstLocker`
  is set (with rank ≥ 3 and `IsWatchedUnlockBankaraMatch`), or when the
  lobby locker loader at `DAT_7106052f98` reports ready (`+0x1a7988` set and
  `+0x1a7980 == +0x60`). Live: rank 99, Bankara 1, FirstLocker 0; loader
  `+0x1a7980 = 0`, `+0x60 = 32`.
- **The locker loader** (`FUN_71022bcd50`, 32 slots, "LocatorLocker") never
  ran its setup (`+0x1a7989 = 0`): it returns early until the locker data
  component's ready byte (`+0x202`, read by `0x7103293c44`) is set.
- **That byte is set only by the answer's parser** (`FUN_7103292c20`): it
  walks the documents in order; entry 0 is stored apart as the player's own
  locker (`Locker`, `LockerInfo`, `PhotoPostPath`, `PhotoPostId`) and, empty
  (no `fields`) or not, sets `+0x201..0x202`. Entries 1–31 fill the lobby
  slots in bands (1–4, 5–15, 16–23, 24–31). **An answer with no documents
  never sets it**, so the loader, the locker room door (`FUN_710397c358`
  checks the same save flags) and the big screen all wait forever.

## What this changes

- [x] Differs: `toyohr.plaza` now answers Locker requests that name players
  with the caller's own entry first, then one entry per named player, an
  empty `Document` for anyone with no locker (`ugcstore.PublishedBy`'s new
  `placeholders`). Canola is unchanged.
- [x] Narrowed the `TODO(capture-needed)` in `internal/toyohr/backlog.go`:
  the caller-first order is read from the parser, not seen in a capture.
