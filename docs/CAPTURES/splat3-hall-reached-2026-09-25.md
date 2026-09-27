# Splatoon 3 reaches the hall on GRID0+ (2026-09-25)

Real console, Splatoon 3 v11.3.0, GRID0+ emuMMC, live GDB attached
(`~/switchnet-re/gdb/grstart.py`). Four server changes, each tested on
hardware in turn; each moved the failure one step later.

| Boot | Server behaviour | What the console did |
| --- | --- | --- |
| 1 | `GetSaveRecord` answers zero messages | no save writes, no `SelectFestSchedule`, GameRecord manager initialised but never started; communication error |
| 2 | `895d697`: empty but existing save record | `SelectFestSchedule`, `WriteSaveRecord evt=InitLeagueSchedule` (stored), Coop attribute read → `NotFound`; **2321-0384** |
| 3 | `5afc318`: Vs/Coop attribute documents created on first read (season 16 / empty) | stored record served back; Vs and Coop fetches run; `WeaponPowerMeasurement/16` → `NotFound`; **2000-0000**, no crash under GDB |
| 4 | `e8b781b`: absent GameRecord documents answered named and empty | **hall reached**, then `InitializeAttributes initial_vs {season 16, rate 1700}` ~10×/s forever; 2321-4992 after ~120 s |
| 5 | `d6de9ae`: `initial_vs` stores the rate as `mmr_rate_<mode>` and sets `mmr_initialized_<mode>` for tw/ar/lf/gl/cl | one `InitializeAttributes`, then **matchmaking**: Create → Track (SEARCHING) → Cancel, as in the working capture |

What each settles:

- **An established console only writes its save after seeing an existing
  record.** Zero messages (Nextendo's measurement for a fresh Nintendo
  account) made this console write nothing, so persistence (`0379ec0`) never
  had anything to keep.
- **A console that has a save expects its two attribute documents to
  exist** and never calls `InitializeAttributes`, matching Nextendo's notes
  on real Nintendo sessions. Sparse documents (only `season_id` for Vs) are
  enough to get in; the game fills its own defaults.
- **Absent GameRecord documents must be named and empty, not NotFound.**
  Hammer documents stay NotFound and are fine.
- **`initial_vs` must leave every mode's rating initialised.** The console
  checks `mmr_initialized_<mode>`; the rate it sends goes in
  `mmr_rate_<mode>`.

GDB detail: the GameRecord manager's Initialize (`0x328b7c8`) ran with a live
client on every boot; the Vs/Coop fetch entries (`0x3283740`, `0x3283d20`)
only ran once the save record existed.

## Root cause, found later the same day: `content-length: 0` behind nginx

At the next Salmon Run shift change (16:00 UTC) both accounts started failing
again. Live GDB on the game's GameRecord step machine (`0x32bd1e4`) and its
point-card job (completion check at `0x32802bc`) showed why: the job sets its
status (`+280`) to 8 only if each sub-request (Regular, Limited, Total) ends
with gRPC status 0 or 5, and our point-card `NotFound`s arrived as **status 2
(UNKNOWN)**.

The reply on the npln → nginx leg was correct (two HEADERS frames,
`grpc-status: 5`, NError detail matching Nintendo's). But its opening headers
carried `content-length: 0`, copied from Nintendo's capture, and nginx's
`grpc_pass` treats that as a finished response and never forwards the
trailers. Nextendo sat behind Traefik, which forwards them. Removing the
header (`a158dd3`) turned the statuses into 5 and the hall worked again on
both accounts.

This likely also explains earlier observations: the zero-message
`GetSaveRecord` (sent with the same header) producing no save writes, and
`WeaponPowerMeasurement` needing a named-empty document instead of
`NotFound`. Both workarounds are worth re-testing against the captured
Nintendo shapes now that `NotFound` reaches the console intact.

Server-side rule: **never send `content-length` on a gRPC reply that goes
through nginx.** `internal/npln` tests assert its absence.
