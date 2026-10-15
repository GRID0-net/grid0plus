"""Source inside a GDB session initialized by splat3_readiness.py.
Read the roster property consumed by FUN_71015173d0, without invoking game code.
Only counts, state flags and hashed member identifiers are logged.
"""
import hashlib


def unsigned(address):
    return struct.unpack('<I', read(address, 4))[0]


def short(address):
    return struct.unpack('<H', read(address, 2))[0]


def tree_find(node, key):
    seen = set()
    for _ in range(64):
        if not node:
            return 0
        if node in seen:
            raise RuntimeError('Cycle in roster tree')
        seen.add(node)
        value = unsigned(node + 0x20)
        if value == key:
            return qword(node + 0x28)
        node = qword(node + (0x8 if key < value else 0x10))
    raise RuntimeError('Roster tree exceeds bounded traversal')


def source_snapshot():
    registry = qword(qword(base + 0x5F1E298))
    session = tree_find(qword(registry + 0x20), 1)
    if not session:
        return dict(status='no_session')
    if not read(session + 0xC, 1)[0]:
        return dict(status='session_inactive', session_object=hex(session))
    properties = qword(session + 0x10)
    prop = tree_find(qword(properties + 0x10), 0xC056FC0F)
    if not prop:
        return dict(status='property_absent', session_object=hex(session))
    fields = dict(session_object=hex(session), property_object=hex(prop),
                  property_type=integer(prop + 8), property_ready=read(prop + 0x25, 1)[0])
    payload = qword(prop + 0x10)
    if not payload or fields['property_type'] != 800 or not fields['property_ready']:
        return dict(status='property_unready', **fields)
    array = qword(payload + 0x20)
    capacity, head, count = [integer(payload + off) for off in (0x28, 0x2C, 0x30)]
    fields.update(payload=hex(payload), capacity=capacity, head=head, count=count)
    if not (0 <= count <= capacity <= 10 and 0 <= head < max(capacity, 1)):
        return dict(status='invalid_collection', **fields)
    marker = qword(base + 0x5F229E0)
    marker_id = read(marker, 10)
    network = qword(qword(base + 0x5F3CA80))
    table = qword(qword(network + 0x50) + 0xE8)
    table_count = integer(table + 0x28)
    if not 0 <= table_count <= 64:
        return dict(status='invalid_station_table', table_count=table_count, **fields)
    table_array = qword(table + 0x30)
    stations = []
    for i in range(table_count):
        node = qword(table_array + i * 8)
        stations.append((read(node + 0x10, 10), integer(node + 8), read(node + 0x20, 1)[0]))
    members = []
    computed = 0
    for i in range(count):
        entry = array + ((head + i) % capacity) * 0x48
        identifier = read(entry + 0x30, 10)
        matches_marker = identifier == marker_id
        matches = [(state, flag) for sid, state, flag in stations if sid == identifier]
        active = [state for state, flag in matches if not flag]
        counted = not matches_marker and bool(active) and active[0] != 3
        computed += int(counted)
        members.append(dict(id_hash=hashlib.sha256(identifier).hexdigest()[:12],
                            marker=matches_marker, states=matches, counted=counted))
    return dict(status='ok', members=members, computed_connected=computed,
                station_count=table_count, **fields)
