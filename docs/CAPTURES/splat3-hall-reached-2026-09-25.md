# Splatoon 3 reaches the hall on SwitchNet (2026-09-25)

Real console, Splatoon 3 v11.3.0, SwitchNet emuMMC, live GDB attached
(`~/switchnet-re/gdb/grstart.py`). Four server changes, each tested on
hardware in turn; each moved the failure one step later.

| Boot | Server behaviour | What the console did |
| --- | --- | --- |
| 1 | `GetSaveRecord` answers zero messages | no save writes, no `SelectFestSchedule`, GameRecord manager initialised but never started; communication error |
| 2 | `895d697`: empty but existing save record | `SelectFestSchedule`, `WriteSaveRecord evt=InitLeagueSchedule` (stored), Coop attribute read → `NotFound`; **2321-0384** |
| 3 | `5afc318`: Vs/Coop attribute documents created on first read (season 16 / empty) | stored record served back; Vs and Coop fetches run; `WeaponPowerMeasurement/16` → `NotFound`; **2000-0000**, no crash under GDB |
| 4 | `e8b781b`: absent GameRecord documents answered named and empty | **hall reached** |

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

GDB detail: the GameRecord manager's Initialize (`0x328b7c8`) ran with a live
client on every boot; the Vs/Coop fetch entries (`0x3283740`, `0x3283d20`)
only ran once the save record existed.
