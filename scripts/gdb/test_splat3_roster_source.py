"""Exercise missing/expired session ownership without GDB or game dumps."""
import pathlib
import struct
import unittest


class OwnedGroupTests(unittest.TestCase):
    def setUp(self):
        self.memory = {}
        def read(address, size):
            return bytes(self.memory.get(address + i, 0) for i in range(size))
        self.ns = dict(base=0, read=read,
                       qword=lambda a: struct.unpack('<Q', read(a, 8))[0],
                       integer=lambda a: struct.unpack('<i', read(a, 4))[0])
        source = pathlib.Path(__file__).with_name('splat3_roster_source.py')
        exec(compile(source.read_text(), str(source), 'exec'), self.ns)
        for address, value in [(0x61E18A0, 0x1000), (0x1080, 0x2000),
                               (0x32A8, 2), (0x32B0, 0x4000),
                               (0x4000, 1), (0x4008, 0x5000),
                               (0x5000, 0x6000), (0x6000, 0x7000),
                               (0x6008, 0x8000), (0x7010, 0x9000),
                               (0x9030, 0xA000), (0x9038, 0xB000),
                               (0xA0B8, 3)]:
            self.put(address, value)
        for offset, value in enumerate(b'\x06All\x00'):
            self.memory[0xA1A8 + offset] = value
        self.memory[0xA1FA] = self.memory[0xA1FB] = 1

    def put(self, address, value):
        for offset, byte in enumerate(struct.pack('<Q', value)):
            self.memory[address + offset] = byte

    def snapshot(self):
        return self.ns['owned_peer_group_snapshot']()

    def test_current_session_resolves_complete_roster(self):
        row = self.snapshot()
        self.assertEqual((row['status'], row['name'], row['members']), ('ok', 'All', 3))
        self.assertEqual((row['own_member_seen'], row['initial_snapshot_done']), (1, 1))
        self.assertFalse(row['matches_callback'])
        self.ns['peer_group_objects'][0xA000] = 'All'
        self.assertTrue(self.snapshot()['matches_callback'])

    def test_missing_manager_does_not_dereference_session(self):
        self.put(0x61E18A0, 0)
        self.assertEqual(self.snapshot()['status'], 'no_manager')

    def test_inactive_selected_session_does_not_reuse_old_group(self):
        self.put(0x32B8, 1)
        self.assertEqual(self.snapshot()['status'], 'empty_session')

    def test_out_of_range_selection_uses_native_primary_fallback(self):
        self.put(0x32B8, 5)
        self.assertEqual(self.snapshot()['selected'], 0)

    def test_expired_weak_owners_are_rejected(self):
        for address, status in [(0x8008, 'expired_api'), (0xB008, 'expired_group')]:
            with self.subTest(status=status):
                self.put(address, 0xFFFFFFFFFFFFFFFF)
                self.assertEqual(self.snapshot()['status'], status)
                self.put(address, 0)

    def test_invalid_member_count_is_rejected(self):
        self.put(0xA0B8, 257)
        self.assertEqual(self.snapshot()['status'], 'invalid_count')


if __name__ == '__main__':
    unittest.main()
