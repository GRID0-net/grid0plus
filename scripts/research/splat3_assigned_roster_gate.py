"""REJECTED v11.3.0 secondary-group gate; never install or release.

The occupied-Pia-slot experiment was rejected because it grew with connected
peers. Native FUN_71014f9760(manager, 1, 1) instead counts every __us document
in the match group. This candidate needs live timing/group validation before
installation. Attempt 10 found secondary_active=0 on both clients and an
unready secondary watcher. This exact candidate would wait indefinitely.
The active primary group's count also grew from one as clients attached;
it is not the complete assigned matchmaking roster on the early host.
Retained for execution-test reproducibility only.
"""
import importlib.util
from pathlib import Path

ROOT = Path.home() / 'cfw-GRID0plus' / 'toolbox'
spec = importlib.util.spec_from_file_location('smallmatch', ROOT / 'tools/make_s3_smallmatch.py')
legacy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(legacy)
i, b = legacy.instruction, legacy.branch
W1, W2, W3, W4, W5 = legacy.W1, legacy.W2, legacy.W3, legacy.W4, legacy.W5
# Twelve alignment bytes between a RET and the next function's prologue.
W6 = 0x3885214
COUNT = 0x14F9760


def call(at, target):
    return b(at, target) | 0x80000000


BLOCKS = {
    # Preserve the connected result and the live LR across the native call.
    W1: [0xA9BF7BE0, legacy.adrp(W1 + 4, 0x5F22A98, 0), b(W1 + 8, W2)],
    W2: [0xF9454C00, 0xF9400000, b(W2 + 8, W3)],
    W3: [0x52800021, 0x52800022, b(W3 + 8, W4)],
    W4: [call(W4, COUNT), 0x2A0003E8, b(W4 + 8, W5)],
    W5: [0xA8C17BE0, 0x6B08001F, b(W5 + 8, W6)],
    # Missing/negative assigned counts cannot equal a positive connected count.
    W6: [b(W6, legacy.WAIT_MORE, condition=1),
         legacy.cbz(W6 + 4, legacy.WAIT_MORE, 0, False), b(W6 + 8, legacy.WAIT_ENOUGH)],
}
OLD_WAIT_SITES = {rva for rva, _, _ in legacy.WAIT_RECORDS}
RECORDS = [row for row in legacy.RECORDS if row[0] not in OLD_WAIT_SITES]
RECORDS += [(legacy.WAIT_HOOK, i(b(legacy.WAIT_HOOK, legacy.WAIT_MORE, condition=1)),
             i(b(legacy.WAIT_HOOK, W1)))]
RECORDS += [(rva, '00' * 12, ''.join(i(word) for word in words))
            for rva, words in BLOCKS.items()]
