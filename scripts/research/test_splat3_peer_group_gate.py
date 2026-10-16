"""Execute assembled readiness gate, including ownership and ABI restoration.

PYTHONPATH=<unicorn install> python test_splat3_peer_group_gate.py records.json
Native mutex/weak-lock/group-accessor calls are simulated; ARM64 gate and its
atomic shared-owner releases execute. This is not a hardware validation.
"""
import json
import struct
import sys
from unicorn import Uc, UC_ARCH_ARM64, UC_MODE_ARM, UC_HOOK_CODE
from unicorn.arm64_const import *
import splat3_peer_group_gate as patch

B, D, SP = 0x7100000000, 0x7200000000, 0x7300010000
records = json.load(open(sys.argv[1]))['records']


def execute(connected, assigned, flags=0x101, missing=None, selected=0, name=0x6c6c4106, full=8, drop_baseline=False):
    uc = Uc(UC_ARCH_ARM64, UC_MODE_ARM)
    pages = set()
    def write(at, data):
        for page in range(at & ~4095, (at + len(data) + 4095) & ~4095, 4096):
            if page not in pages:
                uc.mem_map(page, 4096)
                pages.add(page)
        uc.mem_write(at, data)
    def q(at, value):
        write(at, struct.pack('<Q', value & 0xffffffffffffffff))
    def getq(at):
        return struct.unpack('<Q', uc.mem_read(at, 8))[0]
    for at, _, value in records:
        write(B + at, bytes.fromhex(value))
    for at in patch.NATIVES.values():
        write(B + at, bytes.fromhex('1f2003d5'))
    write(SP - 0x2000, bytes(0x4000))
    write(D, bytes(0x10000))
    # Use the same ownership chain validated by attempt 12.
    q(B + 0x61e18a0, D)
    root, manager, array = D, D + 0x1000, D + 0x3000
    box, handle, api, api_owner = D + 0x4000, D + 0x5000, D + 0x6000, D + 0x7000
    impl, group, group_owner = D + 0x8000, D + 0x9000, D + 0xa000
    destructor = B + 0x1000000
    write(destructor, bytes.fromhex('1f2003d5'))
    q(api_owner, D + 0xb000)
    q(group_owner, D + 0xb000)
    q(D + 0xb010, destructor)
    q(root + 0x80, manager)
    q(manager + 0x12a8, 2)
    q(manager + 0x12b0, array)
    q(manager + 0x12b8, selected)
    q(array, 1)
    q(array + 8, box)
    q(box, handle)
    q(handle, api)
    q(handle + 8, api_owner)
    q(api + 0x10, impl)
    q(impl + 0x30, group)
    q(impl + 0x38, group_owner)
    q(group + 0xb8, assigned)
    q(group + 0x1a8, name)
    write(group + 0x1fa, struct.pack('<H', flags))
    for tag, at in [('root', B + 0x61e18a0), ('manager', root + 0x80),
                    ('array', manager + 0x12b0), ('entry', array), ('box', array + 8),
                    ('handle', box), ('api', handle), ('api_owner', handle + 8)]:
        if missing == tag:
            q(at, 0)
    if missing == 'api_expired':
        q(api_owner + 8, -1)
    if missing == 'group_expired':
        q(group_owner + 8, -1)
    if missing == 'group':
        q(impl + 0x30, 0)
    if missing == 'group_owner':
        q(impl + 0x38, 0)
    before = (getq(api_owner + 8), getq(group_owner + 8))
    original = connected & 0xffffffff
    uc.reg_write(UC_ARM64_REG_X0, original)
    uc.reg_write(UC_ARM64_REG_SP, SP)
    uc.reg_write(UC_ARM64_REG_LR, B + 0x38a420c)
    saved = {getattr(__import__('unicorn.arm64_const', fromlist=['']), f'UC_ARM64_REG_X{n}'): 0x10000 + n for n in range(19, 30)}
    saved[UC_ARM64_REG_X24] = full
    for reg, value in saved.items():
        uc.reg_write(reg, value)
    locked, calls, hit, destroyed, weak_releases = [], [], [], [], []
    def hook(emu, pc, size, data):
        def result(value):
            ret = emu.reg_read(UC_ARM64_REG_LR)
            for n in range(18):
                reg = getattr(__import__('unicorn.arm64_const', fromlist=['']), f'UC_ARM64_REG_X{n}')
                emu.reg_write(reg, 0xabcd0000 + n)
            emu.reg_write(UC_ARM64_REG_X0, value)
            emu.reg_write(UC_ARM64_REG_PC, ret)
        if pc in (B + patch.legacy.WAIT_MORE, B + patch.legacy.WAIT_ENOUGH):
            hit.append(pc)
            emu.emu_stop()
        elif pc == B + patch.NATIVES['lock_mutex']:
            assert not locked and emu.reg_read(UC_ARM64_REG_X0) == manager + 0x2d0
            locked.append(True)
            result(0)
        elif pc == B + patch.NATIVES['unlock_mutex']:
            assert locked and emu.reg_read(UC_ARM64_REG_X0) == manager + 0x2d0
            locked.pop()
            if drop_baseline:
                # Simulate session replacement dropping its references after
                # the patch acquired independent ownership.
                q(api_owner + 8, getq(api_owner + 8) - 1)
                q(group_owner + 8, getq(group_owner + 8) - 1)
            result(0)
        elif pc == B + patch.NATIVES['weak_lock']:
            assert locked
            owner = emu.reg_read(UC_ARM64_REG_X0)
            assert owner == api_owner
            count = getq(owner + 8)
            if count != 0xffffffffffffffff:
                q(owner + 8, count + 1)
            result(owner if count != 0xffffffffffffffff else 0)
        elif pc == B + patch.NATIVES['get_group']:
            assert locked and emu.reg_read(UC_ARM64_REG_X0) == api
            assert getq(api_owner + 8) == 1
            output = emu.reg_read(UC_ARM64_REG_X8)
            owner = getq(impl + 0x38)
            if owner and getq(owner + 8) != 0xffffffffffffffff:
                q(owner + 8, getq(owner + 8) + 1)
                q(output, getq(impl + 0x30))
                q(output + 8, owner)
            else:
                q(output, 0)
                q(output + 8, 0)
            result(0)
        elif pc == B + patch.NATIVES['release_weak']:
            assert drop_baseline and not locked
            weak_releases.append(emu.reg_read(UC_ARM64_REG_X0))
            result(0)
        elif pc == destructor:
            assert drop_baseline and not locked
            destroyed.append(emu.reg_read(UC_ARM64_REG_X0))
            result(0)
        if pc in [B + at for name, at in patch.NATIVES.items() if name not in ('root_page', 'wait_more', 'wait_enough')]:
            calls.append(pc)
    uc.hook_add(UC_HOOK_CODE, hook)
    uc.emu_start(B + patch.legacy.WAIT_HOOK, B + patch.legacy.WAIT_MORE + 4, count=2000)
    assert len(hit) == 1 and not locked
    assert uc.reg_read(UC_ARM64_REG_SP) == SP
    assert uc.reg_read(UC_ARM64_REG_X0) == original
    assert uc.reg_read(UC_ARM64_REG_LR) == B + 0x38a420c
    for reg, value in saved.items():
        assert uc.reg_read(reg) == value, reg
    expected_counts = tuple((n - 1) & 0xffffffffffffffff for n in before) if drop_baseline else before
    assert (getq(api_owner + 8), getq(group_owner + 8)) == expected_counts
    if drop_baseline:
        assert destroyed == weak_releases == [group_owner, api_owner]
    else:
        assert not destroyed and not weak_releases
    return hit[0] == B + patch.legacy.WAIT_ENOUGH


cases = 0
for full in (4, 8):
    for connected in range(-1, 11):
        for assigned in range(11):
            assert execute(connected, assigned, full=full) == (1 <= assigned <= full and connected == assigned), (full, connected, assigned)
            cases += 1
for flag in (0, 1, 0x100, 0x102):
    assert not execute(3, 3, flags=flag)
    cases += 1
for missing in ('root', 'manager', 'array', 'entry', 'box', 'handle', 'api',
                'api_owner', 'api_expired', 'group_expired', 'group', 'group_owner'):
    assert not execute(3, 3, missing=missing), missing
    cases += 1
assert not execute(3, 3, selected=1)
assert execute(3, 3, selected=2)
assert not execute(3, 3, name=0)
assert execute(3, 3, drop_baseline=True)
print(f'{cases + 4} ARM64 ownership/readiness cases passed')
