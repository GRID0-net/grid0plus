"""REJECTED v11.3.0 roster gate experiment; never install or release.

The real roster has ten slots, including empty IDs. Count non-empty IDs while
FUN_71015173d0 already walks it; return that count in volatile w1 alongside
its unchanged connected count in w0. The caller still uses its 60-update
stability counter. Attempt 9 disproved its premise: occupied IDs can still
contain only the local player after Gamesync delivered all three assigned
users. Retained solely to reproduce the rejected experiment.
"""
import importlib.util
from pathlib import Path

ROOT = Path.home() / 'cfw-GRID0plus' / 'toolbox'
spec = importlib.util.spec_from_file_location('smallmatch', ROOT / 'tools/make_s3_smallmatch.py')
legacy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(legacy)
i = legacy.instruction
b = legacy.branch
W1, W2, W3, W4 = legacy.W1, legacy.W2, legacy.W3, legacy.W4

BLOCKS = {
    W1: [0x2A1C03E1, 0x2A1403E0, b(W1 + 8, 0x15176D4)],
    W2: [0x1100079C, 0xF9402A00, b(W2 + 8, 0x1517674)],
    W3: [0x7100001F, b(W3 + 4, legacy.WAIT_MORE, condition=13), b(W3 + 8, W4)],
    W4: [0x6B01001F, b(W4 + 4, legacy.WAIT_MORE, condition=1), b(W4 + 8, legacy.WAIT_ENOUGH)],
}
EXTRA = [
    # x28 is saved/restored by the original helper and otherwise unused.
    (0x1517438, i(0xD503201F), i(0x2A1F03FC)),
    # Increment only after the original empty-marker comparison has failed.
    (0x1517670, i(0xF9402A00), i(b(0x1517670, W2))),
    # w1 is caller-saved; preserve the helper's existing w0 return value.
    (0x15176D0, i(0x2A1403E0), i(b(0x15176D0, W1))),
    (legacy.WAIT_HOOK, i(b(legacy.WAIT_HOOK, legacy.WAIT_MORE, condition=1)),
     i(b(legacy.WAIT_HOOK, W3))),
]
EXTRA += [(rva, '00' * 12, ''.join(i(word) for word in words))
          for rva, words in BLOCKS.items()]
old_sites = {rva for rva, _, _ in legacy.WAIT_RECORDS}
RECORDS = [row for row in legacy.RECORDS if row[0] not in old_sites] + EXTRA
