package nplnrest

import (
	"encoding/binary"
	"time"
)

// A minimal FlatBuffers writer and reader for the frontline's few message
// shapes. Children are laid out after their parents, so every uoffset is
// positive as the format requires, and each vtable sits just before its
// table. Field types per message come from the npns verifiers listed in
// docs/CAPTURES/npns-sysmodule-decompile.md.

type fbField struct {
	scalar []byte // little-endian value, aligned to its own size
	ref    bool   // a uoffset to something written later
}

type fbWriter struct{ b []byte }

func (w *fbWriter) align(n int) {
	for len(w.b)%n != 0 {
		w.b = append(w.b, 0)
	}
}

// table writes a vtable and its table; refs maps a field index to the slot
// that must later be pointed at a child with patch.
func (w *fbWriter) table(fields []*fbField) (pos int, refs map[int]int) {
	refs = map[int]int{}
	w.align(2)
	vt := len(w.b)
	vtSize := 4 + 2*len(fields)
	w.b = append(w.b, make([]byte, vtSize)...)
	w.align(4)
	pos = len(w.b)
	w.b = append(w.b, 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(w.b[pos:], uint32(int32(pos-vt)))
	offsets := make([]int, len(fields))
	for i, f := range fields {
		if f == nil {
			continue
		}
		size := 4
		if !f.ref {
			size = len(f.scalar)
		}
		// Aligned in the buffer, which npns's verifier checks.
		for len(w.b)%size != 0 {
			w.b = append(w.b, 0)
		}
		offsets[i] = len(w.b) - pos
		if f.ref {
			refs[i] = len(w.b)
			w.b = append(w.b, 0, 0, 0, 0)
		} else {
			w.b = append(w.b, f.scalar...)
		}
	}
	binary.LittleEndian.PutUint16(w.b[vt:], uint16(vtSize))
	binary.LittleEndian.PutUint16(w.b[vt+2:], uint16(len(w.b)-pos))
	for i, off := range offsets {
		binary.LittleEndian.PutUint16(w.b[vt+4+2*i:], uint16(off))
	}
	return pos, refs
}

func (w *fbWriter) str(s string) int {
	w.align(4)
	pos := len(w.b)
	w.b = binary.LittleEndian.AppendUint32(w.b, uint32(len(s)))
	w.b = append(w.b, s...)
	w.b = append(w.b, 0)
	return pos
}

func (w *fbWriter) emptyVector() int {
	w.align(4)
	pos := len(w.b)
	w.b = append(w.b, 0, 0, 0, 0)
	return pos
}

func (w *fbWriter) patch(slot, target int) {
	binary.LittleEndian.PutUint32(w.b[slot:], uint32(target-slot))
}

func u8(v byte) *fbField    { return &fbField{scalar: []byte{v}} }
func u64(v uint64) *fbField { return &fbField{scalar: binary.LittleEndian.AppendUint64(nil, v)} }
func ref() *fbField         { return &fbField{ref: true} }

// frontlineFrame wraps one union body in the root envelope every frontline
// message shares -- type, body, sender "fro", Unix time -- and size-prefixes
// it. body writes the union table and returns its position.
func frontlineFrame(typ byte, now time.Time, body func(w *fbWriter) int) []byte {
	w := &fbWriter{b: make([]byte, 4)}
	root, refs := w.table([]*fbField{u8(typ), ref(), ref(), u64(uint64(now.Unix()))})
	binary.LittleEndian.PutUint32(w.b[0:], uint32(root))
	w.patch(refs[1], body(w))
	w.patch(refs[2], w.str("fro"))
	w.align(4)
	return append(binary.LittleEndian.AppendUint32(nil, uint32(len(w.b))), w.b...)
}

// ---- reading ----

// fbTable resolves a uoffset slot to the table it points at.
func fbTable(p []byte, slot int) (int, bool) {
	if slot < 0 || slot+4 > len(p) {
		return 0, false
	}
	t := slot + int(binary.LittleEndian.Uint32(p[slot:]))
	return t, t+4 <= len(p)
}

// fbFieldPos is the position of field idx in the table at t, or false when
// the field is absent.
func fbFieldPos(p []byte, t, idx int) (int, bool) {
	vt := t - int(int32(binary.LittleEndian.Uint32(p[t:])))
	if vt < 0 || vt+4 > len(p) {
		return 0, false
	}
	vtSize := int(binary.LittleEndian.Uint16(p[vt:]))
	at := vt + 4 + 2*idx
	if 4+2*idx+2 > vtSize || at+2 > len(p) {
		return 0, false
	}
	off := int(binary.LittleEndian.Uint16(p[at:]))
	if off == 0 || t+off >= len(p) {
		return 0, false
	}
	return t + off, true
}

// frontlineBody is the union body table of a frame's payload.
func frontlineBody(p []byte) (int, bool) {
	root, ok := fbTable(p, 0)
	if !ok {
		return 0, false
	}
	slot, ok := fbFieldPos(p, root, 1)
	if !ok {
		return 0, false
	}
	return fbTable(p, slot)
}

func fbU64(p []byte, t, idx int) (uint64, bool) {
	pos, ok := fbFieldPos(p, t, idx)
	if !ok || pos+8 > len(p) {
		return 0, false
	}
	return binary.LittleEndian.Uint64(p[pos:]), true
}

func u16(v uint16) *fbField { return &fbField{scalar: binary.LittleEndian.AppendUint16(nil, v)} }
func i64(v int64) *fbField  { return &fbField{scalar: binary.LittleEndian.AppendUint64(nil, uint64(v))} }
