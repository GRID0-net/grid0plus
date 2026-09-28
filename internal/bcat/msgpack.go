package bcat

import "encoding/binary"

// msgpackWriter handles only the fixed scalar/map/array types BCAT uses.
type msgpackWriter struct{ b []byte }

func newMsgpackWriter() *msgpackWriter  { return &msgpackWriter{} }
func (w *msgpackWriter) bytes() []byte  { return w.b }
func (w *msgpackWriter) mapLen(n int)   { w.b = append(w.b, 0xde, byte(n>>8), byte(n)) }
func (w *msgpackWriter) arrayLen(n int) { w.b = append(w.b, 0xdc, byte(n>>8), byte(n)) }
func (w *msgpackWriter) bool(v bool) {
	if v {
		w.b = append(w.b, 0xc3)
	} else {
		w.b = append(w.b, 0xc2)
	}
}
func (w *msgpackWriter) uint(v uint64) {
	switch {
	case v <= 0x7f:
		w.b = append(w.b, byte(v))
	case v <= 0xff:
		w.b = append(w.b, 0xcc, byte(v))
	case v <= 0xffff:
		w.b = append(w.b, 0xcd, byte(v>>8), byte(v))
	case v <= 0xffffffff:
		w.b = append(w.b, 0xce)
		var x [4]byte
		binary.BigEndian.PutUint32(x[:], uint32(v))
		w.b = append(w.b, x[:]...)
	default:
		w.b = append(w.b, 0xcf)
		var x [8]byte
		binary.BigEndian.PutUint64(x[:], v)
		w.b = append(w.b, x[:]...)
	}
}
func (w *msgpackWriter) str(s string) {
	n := len(s)
	switch {
	case n < 32:
		w.b = append(w.b, 0xa0|byte(n))
	case n <= 0xff:
		w.b = append(w.b, 0xd9, byte(n))
	case n <= 0xffff:
		w.b = append(w.b, 0xda, byte(n>>8), byte(n))
	default:
		w.b = append(w.b, 0xdb)
		var x [4]byte
		binary.BigEndian.PutUint32(x[:], uint32(n))
		w.b = append(w.b, x[:]...)
	}
	w.b = append(w.b, s...)
}
