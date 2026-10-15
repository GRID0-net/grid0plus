"""Source inside a GDB session initialized by splat3_readiness.py.
Read the roster property consumed by FUN_71015173d0, without invoking game code.
Only counts, state flags and hashed member identifiers are logged.
"""
import hashlib
import struct

peer_group_objects = {}


def owned_peer_group_snapshot():
    """Follow the current session's ownership path, not a remembered callback.

    0x44e53f8 selects the session at manager+0x12b8; 0x4026d0
    resolves its API weak pointer and 0x4028dc resolves the API's group.
    This observation does not acquire shared ownership or invoke game code.
    """
    root = qword(base + 0x61E18A0)
    manager = qword(root + 0x80) if root else 0
    if not manager:
        return dict(status='no_manager')
    size = unsigned(manager + 0x12A8)
    selected = unsigned(manager + 0x12B8)
    array = qword(manager + 0x12B0)
    if not array or not 1 <= size <= 2:
        return dict(status='invalid_session_array', size=size)
    selected = selected if selected < size else 0
    entry = array + selected * 0x150
    if not unsigned(entry):
        return dict(status='empty_session', selected=selected)
    box = qword(entry + 8)
    handle = qword(box) if box else 0
    if not handle:
        return dict(status='no_api_handle', selected=selected)
    api, control = qword(handle), qword(handle + 8)
    if not api or not control or qword(control + 8) == 0xFFFFFFFFFFFFFFFF:
        return dict(status='expired_api', selected=selected)
    impl = qword(api + 0x10)
    if not impl:
        return dict(status='no_api_impl', selected=selected)
    group, control = qword(impl + 0x30), qword(impl + 0x38)
    if not group or not control or qword(control + 8) == 0xFFFFFFFFFFFFFFFF:
        return dict(status='expired_group', selected=selected)
    count = qword(group + 0xB8)
    if count > 256:
        return dict(status='invalid_count', members=count)
    return dict(status='ok', selected=selected, object=hex(group),
                name=collection_string(group + 0x1A8).decode('ascii', errors='replace'),
                members=count, own_member_seen=read(group + 0x1FA, 1)[0],
                initial_snapshot_done=read(group + 0x1FB, 1)[0],
                matches_callback=group in peer_group_objects)


def observe_peer_group():
    # The __pus update lambda at 0x424ae0 owns this base-group pointer.
    group = qword(reg('x0') + 0x10)
    if not group:
        return
    name = collection_string(group + 0x1A8)
    if name == b'All':
        peer_group_objects[group] = name.decode('ascii')


def peer_group_snapshot():
    out = []
    for group, name in list(peer_group_objects.items()):
        try:
            # __pus callback inserts into the map at +0xa0; insertion at
            # 0x4252c0 increments map+0x18, hence group+0xb8.
            count = qword(group + 0xB8)
            if count > 256:
                raise RuntimeError('Unexpected peer-group member count')
            out.append(dict(object=hex(group), name=name, members=count,
                            own_member_seen=read(group + 0x1FA, 1)[0],
                            initial_snapshot_done=read(group + 0x1FB, 1)[0]))
        except Exception as exc:
            out.append(dict(object=hex(group), status='read_error', error=str(exc)))
    return out


def collection_string(address):
    """Read the bounded libc++ string returned by FUN_71004009dc."""
    first = read(address, 1)[0]
    if first & 1:
        size = qword(address + 8)
        data = qword(address + 0x10)
    else:
        size = first >> 1
        data = address + 1
    if size > 128:
        raise RuntimeError('Unexpected collection-name length')
    return read(data, size)


def assigned_snapshot():
    """Count __us documents as FUN_71014f9760(..., group, 1) does.

    This source is distinct from Pia's connected-member property. Read the
    document trees without the game's traversal writes or inferior calls.
    Live validation must establish that the selected group is this match.
    """
    lobby_slot = qword(base + 0x5F22A98)
    lobby = qword(lobby_slot) if lobby_slot else 0
    flags = dict(primary_active=read(lobby + 0x20, 1)[0],
                 secondary_active=read(lobby + 0x40, 1)[0],
                 special=read(lobby + 0x1881, 1)[0]) if lobby else {}
    root = qword(base + 0x61E18A0)
    manager = qword(root + 0x80) if root else 0
    if not manager:
        return dict(status='no_gamesync_manager')
    groups = unsigned(manager + 0x12A8)
    array = qword(manager + 0x12B0)
    if not array or not 1 <= groups <= 2:
        return dict(status='unexpected_groups', groups=groups)
    result = []
    for group in range(groups):
        watcher = qword(array + group * 0x150 + 0x140)
        if not watcher:
            result.append(dict(group=group, status='no_watcher'))
            continue
        state = integer(watcher + 0x124)
        selected = unsigned(watcher + 0x1A4)
        selected = selected if selected < 2 else 0
        tree = qword(watcher + 0x138 + selected * 0x20)
        pending = [tree] if tree else []
        seen = set()
        assigned = 0
        while pending:
            node = pending.pop()
            if node in seen or len(seen) >= 256:
                raise RuntimeError('Invalid or oversized Gamesync document tree')
            seen.add(node)
            name = qword(node + 0x28)
            if name and collection_string(name) == b'__us':
                assigned += 1
            for offset in (8, 0x10):
                child = qword(node + offset)
                if child:
                    pending.append(child)
        result.append(dict(group=group, state=state, assigned=assigned,
                           documents=len(seen), buffer=selected))
    selected = result[1 if groups > 1 else 0]
    estimate = -1
    if flags.get('special'):
        estimate = 1
    elif flags.get('secondary_active') and selected.get('state') == 2:
        estimate = selected['assigned']
    return dict(status='ok', groups=result, count_flags=flags,
                secondary_count_estimate=estimate)


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
                occupied=sum(not member['marker'] for member in members),
                distinct_occupied=len({member['id_hash'] for member in members if not member['marker']}),
                station_count=table_count, **fields)
