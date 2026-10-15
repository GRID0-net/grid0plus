"""GDB-only readiness probe for the operator's Splatoon 3 v11.3.0.

Run with GRID0_GDB_TARGET=host:port GRID0_GDB_KIND=switch|ryujinx and
GRID0_GDB_LOG=/private/path.jsonl gdb -q -nx -x this_script.py.
Open the game first. Does not patch guest memory. Ctrl+C, then `delete`,
`detach`, `quit` cleans up. Reattach for another first-entry measurement.
"""
import datetime
import json
import os
from pathlib import Path
import re
import struct

import gdb

kind = os.environ['GRID0_GDB_KIND']
target = os.environ['GRID0_GDB_TARGET']
log_path = os.environ['GRID0_GDB_LOG']
if kind not in ('switch', 'ryujinx'):
    raise ValueError('GRID0_GDB_KIND must be switch or ryujinx')
fd = os.open(log_path, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
log = os.fdopen(fd, 'a', buffering=1)


def emit(event, **fields):
    row = dict(time=datetime.datetime.now(datetime.timezone.utc).isoformat(),
               device=kind, event=event, **fields)
    line = json.dumps(row)
    log.write(line + '\n')
    print(line, flush=True)


def reg(name):
    return int(gdb.parse_and_eval('$' + name))


def read(address, size):
    return gdb.selected_inferior().read_memory(address, size).tobytes()


def qword(address):
    return struct.unpack('<Q', read(address, 8))[0]


def integer(address):
    return struct.unpack('<i', read(address, 4))[0]


def roster():
    slot = qword(base + 0x5F22A98)
    obj = qword(slot) if slot else 0
    return obj, integer(obj + 0x490) if obj else None


gdb.execute('set pagination off')
gdb.execute('set confirm off')
gdb.execute('set architecture aarch64')
gdb.execute('set print thread-events off')
if kind == 'switch':
    gdb.execute('target extended-remote ' + target)
    processes = gdb.execute('info os processes', to_string=True)
    match = re.search(r'^\s*(\d+)\s+Application\b', processes, re.M)
    if not match:
        raise RuntimeError('No running application; start Splatoon 3 before attaching')
    gdb.execute('attach ' + match.group(1))
else:
    gdb.execute('target remote ' + target)
info = gdb.execute('monitor get info', to_string=True)
if '0100c2500fc20000' not in info.lower():
    gdb.execute('detach')
    raise RuntimeError('Target is not Splatoon 3')
module_info = info.split('Modules:', 1)[-1]
modules = re.findall(r'^[ \t]*0x([0-9a-fA-F]+)[ \t]*-[ \t]*0x([0-9a-fA-F]+)[ \t]+(\S+)[ \t]*$', module_info, re.M)
if not modules:
    gdb.execute('detach')
    raise RuntimeError('Cannot identify main module')
base, end, module = max(((int(a, 16), int(b, 16), name)
                         for a, b, name in modules), key=lambda m: m[1] - m[0])
# This instruction check rejects mismatched executables before setting probes.
compare = read(base + 0x38A420C, 4).hex()
if compare not in ('1f00186b', '1f00006b'):
    gdb.execute('detach')
    raise RuntimeError('Unrecognized WaitFullmember instructions: ' + compare)
emit('attached', base=hex(base), module=module, compare=compare)
source_script = Path(__file__).with_name('splat3_roster_source.py')
try:
    exec(compile(source_script.read_text(), str(source_script), 'exec'), globals())
except Exception:
    gdb.execute('detach')
    raise


class ReadyProbe(gdb.Breakpoint):
    def __init__(self, rva, event, first_only=False):
        super().__init__('*' + hex(base + rva), internal=True)
        self.event = event
        self.first_only = first_only
        self.hits = 0

    def stop(self):
        self.hits += 1
        if self.first_only or self.hits >= 8:
            self.enabled = False
        try:
            obj, expected = roster()
            fields = dict(pc=hex(reg('pc') - base), roster_object=hex(obj),
                          legacy_expected=expected, hit=self.hits)
            if self.event != 'network_error':
                fields.update(connected=reg('w0'), full=reg('w24'),
                              stable=integer(reg('x19') + 0x2EC))
                try:
                    fields['source_roster'] = source_snapshot()
                except Exception as exc:
                    fields['source_roster'] = dict(status='read_error', error=str(exc))
            else:
                fields['caller'] = hex(reg('x30') - base)
            emit(self.event, **fields)
        except Exception as exc:
            emit('probe_read_error', probe=self.event, error=str(exc))
        return False


class RosterWatch(gdb.Breakpoint):
    def __init__(self, obj):
        super().__init__('*(unsigned int *)' + hex(obj + 0x490),
                         type=gdb.BP_WATCHPOINT, wp_class=gdb.WP_WRITE, internal=True)
        self.hits = 0

    def stop(self):
        self.hits += 1
        if self.hits >= 32:
            self.enabled = False
        try:
            obj, expected = roster()
            emit('roster_changed', pc=hex(reg('pc') - base),
                 roster_object=hex(obj), expected=expected)
        except Exception as exc:
            emit('probe_read_error', probe='roster_changed', error=str(exc))
        return False


try:
    obj, expected = roster()
    emit('initial_roster', roster_object=hex(obj), legacy_expected=expected)
    ReadyProbe(0x38A420C, 'readiness_entry', first_only=True)
    ReadyProbe(0x38A4228, 'readiness_passed')
    ReadyProbe(0x14F44CC, 'network_error')
    if kind == 'switch' and obj:
        RosterWatch(obj)
    gdb.execute('continue')
except Exception:
    gdb.execute('delete breakpoints')
    gdb.execute('detach')
    raise
