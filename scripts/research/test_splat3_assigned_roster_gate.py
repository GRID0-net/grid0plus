"""Execute the candidate ARM64 gate; mock only the native roster-count call.

PYTHONPATH=<unicorn install> python test_splat3_assigned_roster_gate.py main.flat
These checks establish control flow and ABI preservation, not live roster timing.
"""
import mmap
import struct
import sys

from unicorn import Uc, UC_ARCH_ARM64, UC_MODE_ARM, UC_HOOK_CODE
from unicorn.arm64_const import *
import splat3_assigned_roster_gate as patch

B, D, STACK = 0x7100000000, 0x7200000000, 0x7300008000
with open(sys.argv[1], 'rb') as source:
    image = mmap.mmap(source.fileno(), 0, access=mmap.ACCESS_READ)
    for rva, expected, _ in patch.RECORDS:
        assert image[rva:rva + len(bytes.fromhex(expected))].hex() == expected, hex(rva)
    # Verify the extra alignment cave's immediate function boundaries too.
    assert image[patch.W6 - 4:patch.W6].hex() == 'c0035fd6'
    assert image[patch.W6 + 12:patch.W6 + 16].hex() == 'fd7bbda9'


def execute(connected, assigned):
    uc = Uc(UC_ARCH_ARM64, UC_MODE_ARM)
    pages = set()

    def write(address, data):
        for page in range(address & ~4095, (address + len(data) + 4095) & ~4095, 4096):
            if page not in pages:
                uc.mem_map(page, 4096)
                pages.add(page)
        uc.mem_write(address, data)

    for rva, _, replacement in patch.RECORDS:
        write(B + rva, bytes.fromhex(replacement))
    for rva in (patch.COUNT, patch.legacy.WAIT_MORE, patch.legacy.WAIT_ENOUGH):
        write(B + rva, bytes.fromhex('1f2003d5'))
    write(B + 0x5F22A98, struct.pack('<Q', D))
    write(D, struct.pack('<Q', D + 0x100))
    write(STACK - 0x1000, bytes(0x2000))
    uc.reg_write(UC_ARM64_REG_SP, STACK)
    uc.reg_write(UC_ARM64_REG_X0, connected)
    uc.reg_write(UC_ARM64_REG_LR, B + 0x38A420C)
    saved = {r: 0x10000 + r for r in range(UC_ARM64_REG_X19, UC_ARM64_REG_X28 + 1)}
    saved[UC_ARM64_REG_X29] = 0x29292929
    for r, v in saved.items():
        uc.reg_write(r, v)
    hit = []
    calls = []

    def hook(emu, pc, size, data):
        if pc == B + patch.COUNT:
            assert emu.reg_read(UC_ARM64_REG_X0) == D + 0x100
            assert emu.reg_read(UC_ARM64_REG_W1) == 1
            assert emu.reg_read(UC_ARM64_REG_W2) == 1
            assert emu.reg_read(UC_ARM64_REG_SP) % 16 == 0
            ret = emu.reg_read(UC_ARM64_REG_LR)
            # A genuine call may destroy every caller-saved general register.
            for reg in range(UC_ARM64_REG_X0, UC_ARM64_REG_X18 + 1):
                emu.reg_write(reg, 0xABCD0000 + reg)
            emu.reg_write(UC_ARM64_REG_W0, assigned & 0xFFFFFFFF)
            emu.reg_write(UC_ARM64_REG_PC, ret)
            calls.append(pc)
        elif pc in (B + patch.legacy.WAIT_MORE, B + patch.legacy.WAIT_ENOUGH):
            hit.append(pc)
            emu.emu_stop()

    uc.hook_add(UC_HOOK_CODE, hook)
    uc.emu_start(B + patch.legacy.WAIT_HOOK, B + 0x38A4260, count=100)
    assert len(hit) == len(calls) == 1
    assert uc.reg_read(UC_ARM64_REG_SP) == STACK
    assert uc.reg_read(UC_ARM64_REG_LR) == B + 0x38A420C
    assert uc.reg_read(UC_ARM64_REG_X0) == connected
    for r, v in saved.items():
        assert uc.reg_read(r) == v, ('callee-saved register', r)
    return hit[0] == B + patch.legacy.WAIT_ENOUGH


cases = 0
for assigned in range(-1, 11):
    for connected in range(11):
        assert execute(connected, assigned) == (connected == assigned and connected > 0), (connected, assigned)
        cases += 1
print(f'{cases} ARM64 gate cases passed, including argument, ABI and original-instruction checks')
