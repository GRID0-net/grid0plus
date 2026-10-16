"""Execute the operator's original/patched ARM64 helper, not a Python replacement.

Usage: PYTHONPATH=<unicorn install> python test_splat3_roster_gate.py main.flat
The sole mocked call supplies a roster property snapshot. No dump is copied.
"""
import mmap
import struct
import sys
from pathlib import Path

from unicorn import Uc, UC_ARCH_ARM64, UC_MODE_ARM, UC_HOOK_CODE
from unicorn.arm64_const import *
import splat3_roster_gate_candidate as patch

B = 0x7100000000
D = 0x7200000000
STACK = 0x7300008000
STOP = 0x7400000000
original = open(sys.argv[1], 'rb')
image = mmap.mmap(original.fileno(), 0, access=mmap.ACCESS_READ)
for rva, expected, _ in patch.RECORDS:
    assert image[rva:rva + len(bytes.fromhex(expected))] == bytes.fromhex(expected), hex(rva)


def execute(ids, station_specs, *, head=0, available=True, revised=True):
    uc = Uc(UC_ARCH_ARM64, UC_MODE_ARM)
    pages = set()

    def write(address, data):
        for page in range(address & ~4095, (address + len(data) + 4095) & ~4095, 4096):
            if page not in pages:
                uc.mem_map(page, 4096)
                pages.add(page)
        uc.mem_write(address, data)

    def q(address, value):
        write(address, struct.pack('<Q', value))

    def w(address, value):
        write(address, struct.pack('<I', value & 0xFFFFFFFF))

    write(B + 0x15173D0, image[0x15173D0:0x1517710])
    write(B + 0x38A420C, image[0x38A420C:0x38A4214])
    write(B + patch.legacy.WAIT_MORE, bytes.fromhex('1f2003d5'))
    write(B + patch.legacy.WAIT_ENOUGH, bytes.fromhex('1f2003d5'))
    for rva, _, replacement in patch.RECORDS if revised else patch.legacy.RECORDS:
        write(B + rva, bytes.fromhex(replacement))
    write(STOP, bytes.fromhex('1f2003d5'))
    write(STACK - 0x1000, bytes(0x2000))

    registry, node, session = D + 0x1000, D + 0x2000, D + 0x3000
    network, service, table = D + 0x4000, D + 0x5000, D + 0x6000
    node_array, slots, marker = D + 0x7000, D + 0x9000, D + 0xA000
    q(B + 0x5F1E298, D)
    q(D, registry)
    q(registry + 0x20, node)
    w(node + 0x20, 1)
    q(node + 0x28, session)
    q(session + 8, 10)
    for offset in (0x5F229E8, 0x5F229F0, 0x5F229F8):
        q(B + offset, marker + 0x100)
    q(B + 0x5F229E0, marker)
    write(marker, bytes(16))
    q(B + 0x5F3CA80, D + 8)
    q(D + 8, network)
    q(network + 0x50, service)
    q(service + 0xE8, table)
    w(table + 0x28, len(station_specs))
    q(table + 0x30, node_array)
    for index, (identifier, state, flag) in enumerate(station_specs):
        address = D + 0xB000 + index * 0x40
        q(node_array + index * 8, address)
        w(address + 8, state)
        write(address + 0x10, identifier.to_bytes(10, 'little'))
        write(address + 0x20, bytes([flag]))
    write(slots, bytes(10 * 0x48))
    for index, identifier in enumerate(ids):
        write(slots + ((head + index) % 10) * 0x48 + 0x30,
              identifier.to_bytes(10, 'little'))
    # The old gate's count is zero, matching both measured clients.
    q(B + 0x5F22A98, D + 0x10)
    q(D + 0x10, D + 0xC000)
    w(D + 0xC000 + 0x490, 0)

    def helper(emu, pc, size, user):
        if pc == B + 0x15175E4:
            sp = emu.reg_read(UC_ARM64_REG_SP)
            q(sp + 0x20, slots)
            w(sp + 0x28, 10)
            w(sp + 0x2C, head)
            w(sp + 0x30, 10)
            emu.reg_write(UC_ARM64_REG_W0, int(available))
            emu.reg_write(UC_ARM64_REG_PC, B + 0x15175E8)

    uc.hook_add(UC_HOOK_CODE, helper)
    saved = {UC_ARM64_REG_X19: 0x1919, UC_ARM64_REG_X20: 0x2020,
             UC_ARM64_REG_X21: 0x2121, UC_ARM64_REG_X28: 0x28282828,
             UC_ARM64_REG_X29: 0x2929}
    for register, value in saved.items():
        uc.reg_write(register, value)
    uc.reg_write(UC_ARM64_REG_SP, STACK)
    uc.reg_write(UC_ARM64_REG_LR, STOP)
    uc.emu_start(B + 0x15173D0, STOP, count=20000)
    assert uc.reg_read(UC_ARM64_REG_PC) == STOP, 'helper failed to return'
    for register, value in saved.items():
        assert uc.reg_read(register) == value, ('callee-saved register', register)
    assert uc.reg_read(UC_ARM64_REG_SP) == STACK
    assert struct.unpack('<I', bytes(uc.mem_read(session + 8, 4)))[0] == 10
    connected = uc.reg_read(UC_ARM64_REG_W0)
    occupied = uc.reg_read(UC_ARM64_REG_W1)
    connected = connected if connected < 0x80000000 else connected - 0x100000000
    uc.reg_write(UC_ARM64_REG_W24, 8)
    endpoints = {B + patch.legacy.WAIT_MORE, B + patch.legacy.WAIT_ENOUGH}
    hit = []

    def gate_stop(emu, pc, size, user):
        if pc in endpoints:
            hit.append(pc)
            emu.emu_stop()

    uc.hook_add(UC_HOOK_CODE, gate_stop)
    uc.emu_start(B + 0x38A420C, B + 0x38A4260, count=32)
    assert len(hit) == 1, 'gate did not reach either original destination'
    return connected, occupied, hit[0] == B + patch.legacy.WAIT_ENOUGH


cases = 0
for members in range(1, 11):
    for connected in range(members + 1):
        ids = list(range(1, members + 1))
        specs = [(i, 2 if i <= connected else 3, 0) for i in ids]
        for head in (0, 7):
            actual, occupied, ready = execute(ids, specs, head=head)
            baseline, _, _ = execute(ids, specs, head=head, revised=False)
            assert actual == baseline == connected, (members, connected, head, actual, baseline)
            assert occupied == members, (members, occupied)
            assert ready == (connected == members), (members, connected, ready)
            cases += 1
for ids, specs, available, expected in [
    ([1, 2, 3], [(1, 2, 0)], True, (1, 3, False)),
    ([1, 2, 3], [(1, 2, 0), (2, 2, 1), (3, 2, 0)], True, (2, 3, False)),
    ([1, 2, 3], [], True, (0, 3, False)),
    ([], [], True, (0, 0, False)),
    ([1, 2, 3], [(1, 2, 0)], False, (-1, 0, False)),
]:
    assert execute(ids, specs, available=available) == expected
    cases += 1
old = execute([1, 2, 3], [(1, 2, 0)], revised=False)
new = execute([1, 2, 3], [(1, 2, 0)])
assert old[0] == new[0] == 1 and old[2] and not new[2]
print(f'{cases} original/patched ARM64 cases passed; callee-saved registers, SP and refcount preserved')
print('Old gate starts with 1/3 connected; candidate waits. Three real IDs plus seven empty slots pass only at 3/3.')
