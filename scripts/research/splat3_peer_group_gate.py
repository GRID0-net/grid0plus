"""Experimental v11.3.0 readiness gate; not installed or hardware validated.

Build only against the operator's flat text and ELF. Keeps the legacy patch's
other changes, replacing its readiness trampoline. Native weak locks protect
both API and peer-group lifetime; manager mutex protects session selection.
"""
import importlib.util
import pathlib
import re
import struct
import subprocess
import tempfile

ROOT = pathlib.Path.home() / 'cfw-GRID0plus/toolbox'
spec = importlib.util.spec_from_file_location('legacy', ROOT / 'tools/make_s3_smallmatch.py')
legacy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(legacy)

ASSEMBLY = '''
gate:
stp x0, x30, [sp, #-128]!
stp x19, x20, [sp, #16]
stp x21, x22, [sp, #32]
stp x23, x24, [sp, #48]
stp x25, x26, [sp, #64]
mov w19, w0
mov w26, w24
mov x22, xzr
mov x23, xzr
mov w25, wzr
cmp w19, #1
b.lt finish
adrp x8, root_page
ldr x8, [x8, #0x8a0]
cbz x8, finish
ldr x20, [x8, #0x80]
cbz x20, finish
add x21, x20, #0x2d0
mov x0, x21
bl lock_mutex
ldr w8, [x20, #0x12a8]
cbz w8, unlock
cmp w8, #2
b.hi unlock
ldr w9, [x20, #0x12b8]
cmp w9, w8
csel w9, w9, wzr, lo
ldr x10, [x20, #0x12b0]
cbz x10, unlock
mov w8, #0x150
umaddl x10, w9, w8, x10
ldr w8, [x10]
cbz w8, unlock
ldr x10, [x10, #8]
cbz x10, unlock
ldr x24, [x10]
cbz x24, unlock
ldr x0, [x24, #8]
cbz x0, unlock
bl weak_lock
mov x22, x0
cbz x0, unlock
ldr x0, [x24]
cbz x0, unlock
add x8, sp, #96
bl get_group
ldp x24, x23, [sp, #96]
unlock:
mov x0, x21
bl unlock_mutex
cbz x23, finish
cbz x24, finish
ldrh w8, [x24, #0x1fa]
cmp w8, #0x101
b.ne finish
ldr w8, [x24, #0x1a8]
mov w9, #0x4106
movk w9, #0x6c6c, lsl #16
cmp w8, w9
b.ne finish
ldr x8, [x24, #0xb8]
cmp x8, #1
b.lo finish
cmp x8, x26
b.hi finish
mov w25, w8
finish:
mov x0, x23
bl release_owner
mov x0, x22
bl release_owner
cmp w19, w25
cset w8, eq
cmp w25, #0
csel w8, w8, wzr, ne
ldp x25, x26, [sp, #64]
ldp x23, x24, [sp, #48]
ldp x21, x22, [sp, #32]
ldp x19, x20, [sp, #16]
ldp x0, x30, [sp], #128
cbz w8, wait_more
b wait_enough
release_owner:
cbz x0, release_return
stp x19, x30, [sp, #-16]!
mov x19, x0
add x8, x0, #8
release_retry:
ldaxr x9, [x8]
sub x10, x9, #1
stlxr w11, x10, [x8]
cbnz w11, release_retry
cbnz x9, release_done
ldr x8, [x19]
ldr x8, [x8, #0x10]
blr x8
mov x0, x19
bl release_weak
release_done:
ldp x19, x30, [sp], #16
release_return:
ret
'''
NATIVES = dict(lock_mutex=0x4E8BF30, unlock_mutex=0x4E8BF40,
               weak_lock=0x4E8C190, get_group=0x4028DC,
               release_weak=0x4E8B360, wait_more=legacy.WAIT_MORE,
               wait_enough=legacy.WAIT_ENOUGH, root_page=0x61E1000)


def build(flat_path, elf_path):
    flat = pathlib.Path(flat_path).read_bytes()
    elf = pathlib.Path(elf_path).read_bytes()
    # Keep conditional branches within +/-1 MiB. Never reuse another patch's
    # padding or a directly referenced address. Inputs stay private.
    occupied = {rva for rva, _, _ in legacy.RECORDS}
    words = memoryview(flat)[:len(flat)//4*4].cast('I')
    targets = set()
    for index, word in enumerate(words):
        at = index * 4
        if word & 0x7C000000 == 0x14000000:
            imm = word & 0x3FFFFFF
            targets.add(at + (imm - 0x4000000 if imm & 0x2000000 else imm) * 4)
        elif word & 0xFF000010 == 0x54000000 or word & 0x7E000000 == 0x34000000:
            imm = (word >> 5) & 0x7FFFF
            targets.add(at + (imm - 0x80000 if imm & 0x40000 else imm) * 4)
        elif word & 0x7E000000 == 0x36000000:
            imm = (word >> 5) & 0x3FFF
            targets.add(at + (imm - 0x4000 if imm & 0x2000 else imm) * 4)
    caves = []
    lo, hi = 0x3820000, 0x3920000
    for match in re.finditer(b'\xc0\x03\x5f\xd6' + b'\0' * 12, flat[lo:hi]):
        at = lo + match.start() + 4
        if at % 4 or any(at <= rva < at + 12 for rva in occupied | targets):
            continue
        if any(struct.pack('<Q', rva) in elf for rva in range(at, at + 12, 4)):
            continue
        caves.append(at)
    lines, labels = [], {}
    for line in ASSEMBLY.strip().splitlines():
        if line.endswith(':'):
            labels.setdefault(len(lines), []).append(line)
        else:
            lines.append(line)
    needed = (len(lines) + 1) // 2
    if len(caves) < needed:
        raise RuntimeError(f'Need {needed} unreferenced caves, found {len(caves)}')
    caves = caves[:needed]
    asm = ['.text']
    for index, address in enumerate(caves):
        asm.append(f'.org {address}')
        asm.append(f'cave_{index}:')
        for slot in range(2):
            pos = index * 2 + slot
            asm.extend(labels.get(pos, []))
            asm.append(lines[pos] if pos < len(lines) else 'nop')
        asm.append(f'b cave_{index + 1}' if index + 1 < needed else 'nop')
    with tempfile.TemporaryDirectory(prefix='grid0-peer-gate-') as folder:
        src, obj, out = [pathlib.Path(folder) / name for name in ('gate.s', 'gate.elf', 'gate.bin')]
        src.write_text('\n'.join(asm) + '\n')
        symbols = [f'-Wl,--defsym={name}={address}' for name, address in NATIVES.items()]
        subprocess.run(['clang', '--target=aarch64-none-elf', '-nostdlib', '-fuse-ld=lld',
                        '-Wl,-Ttext=0', '-Wl,--image-base=0', '-Wl,--entry=0', *symbols, str(src), '-o', str(obj)], check=True)
        subprocess.run(['llvm-objcopy', '-O', 'binary', '--only-section=.text', str(obj), str(out)], check=True)
        image = out.read_bytes()
    old_sites = {rva for rva, _, _ in legacy.WAIT_RECORDS}
    records = [row for row in legacy.RECORDS if row[0] not in old_sites]
    records.append((legacy.WAIT_HOOK, legacy.instruction(legacy.branch(legacy.WAIT_HOOK, legacy.WAIT_MORE, condition=1)),
                    legacy.instruction(legacy.branch(legacy.WAIT_HOOK, caves[0]))))
    records.extend((at, '00' * 12, image[at:at + 12].hex()) for at in caves)
    for at, expected, _ in records:
        if flat[at:at + len(bytes.fromhex(expected))].hex() != expected:
            raise RuntimeError(f'Unexpected original bytes at {at:#x}')
    return records, caves
