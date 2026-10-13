# Splatoon 3 `save.dat`: container and encryption (2026-10-13)

**Source:** Splatoon 3 11.3.0 `main` (build
`28C4287AEE36F7499DA60F3E68B54C70DA382D75`), read live in the GRID0
Ryujinx fork. A temporary log of the guest stack behind each `save.dat`
read found the loader on the game's `SaveDataThread`; its code was then
disassembled from guest memory, because Ghidra had never disassembled
that region (why every static search missed it). Verified on three real
saves (Ryujinx, Citron, Eden).

**Nothing here is key material.** The 32-word table the scheme depends on
is the game's and stays out of this repository (CLAUDE.md rule 1): an
operator file, the way `internal/bcat` loads its IV table.

## Container

| offset | size | field |
| --- | --- | --- |
| 0x0 | 4 | `seed`, u32 little-endian |
| 0x4 | 4 | `check`, u32 little-endian |
| 0x8 | rest | body, AES-256-CTR ciphertext |

No padding: AES-CTR is a stream, which is why the sizes are not multiples
of 16 and nothing reads as plaintext from byte 0.

## Key, IV and check

`sead::Random(s)` is sead's xorshift128 seeded the usual way
(`x[i] = (prev ^ prev >> 30) * 0x6c078965 + i`, i = 1..4, starting from s).

1. `v = table[Random(seed).u32() >> 27]` — one of 32 constants in `main`
   (0x71050b6dec).
2. `R = Random(v)`; 48 draws, each byte the draw's top 8 bits: the first
   32 are the AES-256 key, the next 16 the CTR IV
   (`FUN_71011955dc`, next to the Splatoon 2 PrevSD decryptor).
3. `check == murmur3_32(body ciphertext, seed=seed) + Random(v).u32()`
   (the first draw of a fresh `Random(v)`), checked before decrypting
   (0x71034cf778 onward).

Decryption uses `nn::crypto::AesImpl<32>::Initialize` and
`CtrModeImpl<AesEncryptor<32>>::Update` into a 1 MiB buffer.

## Plaintext

Starts `4d 53 00 04` — `MS`, version 4, which the loader checks before
anything else. The body is MessagePack-shaped: most values are `0xce` +
u32, keys appear to be 32-bit hashes rather than names, and only a few
strings (NPC names such as `Bukichi`, `Hotaru`) are plain. **Keys are `murmur3_32(name, seed 0)`, stored big-endian** (as MessagePack
`0xce` u32s): `Money`, `PlayerRank`, `UserName`, `HaveWeaponMap`,
`LastPlayHaveBadgeMap`, `Udemae`, `Exp` and `Common` (offset 10, the first
key after the header) all hash to values present in a decrypted save. The
CloudSave records GRID0+ already stores carry every server-side key by
name, so hashing those names gives the reverse map for TODO.md §1.6.

## Structure and key spelling

After the 8-byte header (`MS`, version 4, then the game version, e.g.
`00 0b 03 00` for 11.3.0) come two MessagePack maps back to back, ending
exactly at the end of the file:

1. the local half (13 keys: `Common`, `LobbyVersus`, `Coop`, `Fest`, ...),
   which never leaves the console;
2. the server half (87 keys in an 11.3.0 save: `Money`, `PlayerRank`,
   `UserName`, `HaveWeaponMap`, ...), which is the CloudSave record.

How a map's keys become record keys, checked against all 13 records on
grid0:

- the value of a field whose name ends in `Map` is keyed by ids, spelled as
  unsigned decimal strings, even where an id is itself a hash of a name
  (`CloudRandomSet.Map` keys hash to `Lot`, `Vendor`);
- any other map is a struct when every key is a known name hash, and an id
  map when none is (`Shop.IsReadWeaponExp`, `FestRecord`);
- values keep their wire type; `float32` (`MaxWeaponPower`) is a
  `DoubleValue` in the record; an empty array is stored as null.

`internal/splat3save` implements this; its `names.txt` lists the field
names.
