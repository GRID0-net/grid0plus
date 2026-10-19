# splat3save

Turns a Splatoon 3 `save.dat` into the cloud save record that the `CloudSave` service in [`internal/toyohr`](../toyohr/README.md) serves
(`tenants/<t>/saveRecords/<uid>`), so a player can bring a save they already have to GRID0+. The bot API route that uses it is described in
[DISCORD_BOT.md](../../docs/DISCORD_BOT.md#splatoon-3-saves).

No Nintendo endpoint is reimplemented here.

## Why an import replaces the record

A console calls `GetSaveRecord` on every boot and takes the server's record over its own save. An uploaded save therefore only reaches the game
through the record, and it has to replace the record whole. Merging would bring back fields from the old save.

## What isn't in this repository

The 32-word table the save key is chosen from is the game's own key material, so it is never committed. The operator supplies it as a 128-byte file
(`baas.bot_api.splatoon3_save_table`), the same way `internal/bcat` takes its IV table. Without it the import is off. The tests use a made-up table.

`names.txt` is only field names: the save stores `murmur3_32(name, 0)` and the
record spells the name out. It holds every field seen in decoded saves and in
stored records. The operator can add more with `baas.bot_api.splatoon3_save_names`,
one name per line. A list of every identifier in the game's main binary
resolves fields newer than this list without a release. It is cut from a game
dump, so it stays an operator file, like the key table.

A field still unknown is left out of the record and reported (`Info.Unknown`),
not refused. The import then keeps the uploaded save and the list under
`data/splat3-save-review/<user>/<time>/`. To bring those fields back: find
each hash's name (hash the game's strings), add it to `names.txt` or the
operator list, and import the kept `save.dat` again.

A map none of whose names is known would otherwise pass for an id map and be
stored under numbers. So a map with a key of 2^24 or more is taken for a
struct of unknown fields; game ids stay below that. The exception is a field
named `...Map` or `...Map_<mode>` (`PaletteRewardAcceptFlagMap_Sdodr`), whose
ids are themselves hashes.

## How a map's keys are spelled

This is decided per map from what a stored record does:

- the value of a field ending in `Map`, or `Map_<mode>`, is keyed by ids, written as unsigned
  decimal strings — even when an id is itself a hash of a name
  (`CloudRandomSet.Map`);
- any other map is a struct if every key is a known name, an id map if none
  is (`Shop.IsReadWeaponExp`), and refused if it is a mix.

Values keep their MessagePack type: integers stay integers (unsigned values stay positive), `float32` becomes a double, and strings, booleans, maps and arrays
carry over. An empty array becomes null, as CloudSave stores a console's own. Where no stored record shows a value's type, it is taken from the wire.

The local half of the save, which is never sent to the server, is skipped.
