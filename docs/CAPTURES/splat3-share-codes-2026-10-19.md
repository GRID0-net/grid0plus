# Replay and scenario codes — how Splatoon 3 types and names them

| | |
| --- | --- |
| Host | the NPLN gRPC front end (`nn.npln.toyohr.v1.Replay`, `.CoopScenario`) |
| System version | 22.5.0 |
| Title | Splatoon 3 `0100c2500fc20000`, 11.3.0 (`main` build 28C4287A…) |
| Captured | 2026-10-19 |
| Tool | static RE of `main` (Ghidra, base `0x7100000000`) |

## What was read

- **The resource name is built by the game, not echoed back.**
  `FUN_71007d8280` streams `"tenants"`, `"/"`, the tenant, `"/replayCodes"`,
  `"/"`, then the code; the function at `0x71007d8b04` does the same with
  `"/coopScenarioCodes"`. Its caller (`FUN_7103299110`) copies the text the
  player typed into it unchanged: no case folding, no dash handling, no
  length or character check of its own.
- **The keyboard is the only filter.** `KeyboardHelper` (`0x7103887880`)
  switches on a mode (`+0x48`, 1–7). Modes 3 and 4 — the two 16-character
  code inputs — call `nn::swkbd::MakePreset(arg, 3)` (`Preset_DownloadCode`),
  then set `textMaxLength = textMinLength = 16`, `isUseUtf8 = 1` and a
  grouping at positions 3, 7 and 11 (the dashes are drawn by the keyboard,
  not part of the returned text). No text-check callback is installed for
  them, unlike the name modes (2 and 6: 1–10 characters, callback
  `0x7103886620`; 7: 1–32, callback `0x7103886af0`).

No mode takes a room code: Splatoon 3 has no typed room-code entry, so
the room code GRID0+ mints (`internal/npln/rooms.go`) is display-only.

So the code a console sends is exactly 16 characters, without dashes, in
whatever character set the system keyboard's download-code preset allows.

## What this changes

- [x] Matches the implementation — `shareCodeFrom` already takes the last
  name segment, drops dashes, upper-cases and requires 16 characters, and
  `RegisterDocument` answers `tenants/<t>/{replayCodes,coopScenarioCodes}/<code>`.
- [x] Narrowed both `TODO(capture-needed)` comments in
  `internal/toyohr/backlog.go` to the one open point.

**Still open:** the download-code character set itself. It lives in the
`swkbd` applet, not in `main`, so the game cannot settle it. GRID0+'s
generated codes avoid I, O and Z; if the applet refuses any character in
`scenarioCodeAlphabet`, a player could not type that code back in.
