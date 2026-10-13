# splat3save

Turns a Splatoon 3 `save.dat` into the cloud save record CloudSave serves
(`internal/toyohr`, `tenants/<t>/saveRecords/<uid>`), so a player can bring a
save they already have to GRID0+ (TODO.md §1.6).

No Nintendo endpoint is reimplemented here. The format was read from the game
itself; see
[`docs/CAPTURES/splat3-save-dat-format-2026-10-13.md`](../../docs/CAPTURES/splat3-save-dat-format-2026-10-13.md).

## Why an import replaces the record

A console calls `GetSaveRecord` on every boot and takes the server's record
over its own save (Nextendo's seeding incident, TODO.md §1.6). An uploaded
save therefore only reaches the game through the record, and it has to
replace it whole: merging would bring back fields from the old save.

## What is not in this repository

The 32-word table the key is chosen from is the game's key material. The
operator supplies it as a 128-byte file (`baas.bot_api.splatoon3_save_table`);
without it the import is off. Tests use a made-up table.

`names.txt` is only field names: the save stores `murmur3_32(name, 0)` and the
record spells the name out. It holds every field seen in a decoded 11.3.0
save and in a stored record. A save carrying a field the list does not know
is refused rather than imported with that field lost.

## How a map's keys are spelled

Decided per map, from what a stored record does:

- the value of a field ending in `Map` is keyed by ids, written as unsigned
  decimal strings — even when an id is itself a hash of a name
  (`CloudRandomSet.Map`);
- any other map is a struct if every key is a known name, an id map if none
  is (`Shop.IsReadWeaponExp`), and refused if it is a mix.

Values keep their MessagePack type: integers as `IntegerValue` (unsigned
`0xce` values stay positive, as the record has them), `float32` as
`DoubleValue`, strings, booleans, maps and arrays; an empty array becomes
null, as CloudSave stores a console's own. Checked against the 13 records on
grid0: every path shape they hold is in a decoded save with the same type.
About 170 shapes no record holds yet take their type from the wire
(`TODO(capture-needed)` in `convertValue`).

The local half of the save (the first map, 13 keys such as `Coop`,
`LobbyVersus`) is never sent to the server and is skipped.
