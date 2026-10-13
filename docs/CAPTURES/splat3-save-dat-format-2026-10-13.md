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
strings (NPC names such as `Bukichi`, `Hotaru`) are plain. Mapping the
hashes back to the CloudSave record's key names is the next step for
TODO.md §1.6; murmur3_32 with seed 0 is how the game hashes its other
names (`ApplicationErrorId`) and is the first thing to try.
