package nex

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

// This file is NEX's serialisation layer: the byte stream every RMC body is
// written in. Reference: https://github.com/kinnay/NintendoClients/wiki/NEX-Common-Types
//
// Only the Switch profile is implemented, and that is deliberate rather than
// an omission: every title this package serves is a Switch title, and the
// profile's two choices -- 8-byte PIDs and a version header on every
// structure (NEX >= 3.5) -- are not negotiated on the wire, so a server
// that got them wrong would fail to parse its first request rather than
// degrade. Supporting the 3DS/Wii U profile would be a second set of paths
// nothing exercises.

// ErrShortRead is returned when a stream ends before a value it was asked to
// read. Every Reader method is sticky on it: after the first failure the rest
// return zero values, and Err reports what went wrong, so a handler can read a
// whole request and check once.
var ErrShortRead = errors.New("nex: stream ended early")

// Writer serialises NEX values, little endian.
type Writer struct {
	buf []byte
}

// NewWriter returns an empty writer.
func NewWriter() *Writer { return &Writer{} }

// Bytes returns everything written so far.
func (w *Writer) Bytes() []byte { return w.buf }

// Len is the number of bytes written.
func (w *Writer) Len() int { return len(w.buf) }

func (w *Writer) Raw(b []byte)       { w.buf = append(w.buf, b...) }
func (w *Writer) U8(v uint8)         { w.buf = append(w.buf, v) }
func (w *Writer) U16(v uint16)       { w.buf = binary.LittleEndian.AppendUint16(w.buf, v) }
func (w *Writer) U32(v uint32)       { w.buf = binary.LittleEndian.AppendUint32(w.buf, v) }
func (w *Writer) U64(v uint64)       { w.buf = binary.LittleEndian.AppendUint64(w.buf, v) }
func (w *Writer) S8(v int8)          { w.U8(uint8(v)) }
func (w *Writer) S16(v int16)        { w.U16(uint16(v)) }
func (w *Writer) S32(v int32)        { w.U32(uint32(v)) }
func (w *Writer) S64(v int64)        { w.U64(uint64(v)) }
func (w *Writer) F64(v float64)      { w.U64(math.Float64bits(v)) }
func (w *Writer) PID(v uint64)       { w.U64(v) }
func (w *Writer) Result(code uint32) { w.U32(code) }

// Bool is one byte, 0 or 1.
func (w *Writer) Bool(v bool) {
	if v {
		w.U8(1)
		return
	}
	w.U8(0)
}

// String is a u16 length that COUNTS the NUL terminator, then the UTF-8 bytes
// and the terminator. The empty string is therefore length 1 and one NUL
// byte, never length 0: that is what NintendoClients writes for "" and what a
// console reads back as an empty string. A length-0 string exists on the wire
// (NintendoClients writes it for None), but a Go string has no None.
func (w *Writer) String(s string) {
	w.U16(uint16(len(s) + 1))
	w.buf = append(w.buf, s...)
	w.buf = append(w.buf, 0)
}

// Buffer is a u32 length and the bytes.
func (w *Writer) Buffer(b []byte) {
	w.U32(uint32(len(b)))
	w.buf = append(w.buf, b...)
}

// QBuffer is a u16 length and the bytes.
func (w *Writer) QBuffer(b []byte) {
	w.U16(uint16(len(b)))
	w.buf = append(w.buf, b...)
}

// DateTime writes NEX's packed date/time value.
func (w *Writer) DateTime(d DateTime) { w.U64(uint64(d)) }

// StationURL writes a station URL as its string form.
func (w *Writer) StationURL(u *StationURL) {
	if u == nil {
		w.String("")
		return
	}
	w.String(u.String())
}

// Struct writes one level of a structure: a version byte, then the content
// as a u32-length buffer. A structure that inherits another writes the parent
// level first and each level gets its own header -- call Struct once per
// level, parent first.
func (w *Writer) Struct(version uint8, content func(*Writer)) {
	inner := NewWriter()
	if content != nil {
		content(inner)
	}
	w.U8(version)
	w.Buffer(inner.buf)
}

// AnyData writes an AnyDataHolder: the type name, then the object wrapped in
// two lengths (the outer one counting the inner length field itself).
func (w *Writer) AnyData(typeName string, object func(*Writer)) {
	inner := NewWriter()
	if object != nil {
		object(inner)
	}
	w.String(typeName)
	w.U32(uint32(inner.Len() + 4))
	w.Buffer(inner.buf)
}

// Variant writes a Variant.
func (w *Writer) Variant(v Variant) {
	w.U8(uint8(v.Type))
	switch v.Type {
	case VariantNone:
	case VariantInt64:
		w.S64(v.Int)
	case VariantDouble:
		w.F64(v.Double)
	case VariantBool:
		w.Bool(v.Bool)
	case VariantString:
		w.String(v.String)
	case VariantDateTime:
		w.DateTime(v.DateTime)
	case VariantUInt64:
		w.U64(v.UInt)
	}
}

// WriteList writes a u32 count and each element.
func WriteList[T any](w *Writer, items []T, each func(*Writer, T)) {
	w.U32(uint32(len(items)))
	for _, it := range items {
		each(w, it)
	}
}

// Reader parses NEX values. See ErrShortRead for its error handling.
type Reader struct {
	buf []byte
	off int
	err error
}

// NewReader reads b.
func NewReader(b []byte) *Reader { return &Reader{buf: b} }

// Err reports the first failure, if any.
func (r *Reader) Err() error { return r.err }

// Remaining is the number of unread bytes.
func (r *Reader) Remaining() int { return len(r.buf) - r.off }

// Fail records err unless an earlier failure is already recorded. Handlers use
// it to reject a value that parsed but makes no sense (a count that cannot
// fit, a type id that does not exist).
func (r *Reader) Fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

func (r *Reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || r.off+n > len(r.buf) {
		r.err = ErrShortRead
		r.off = len(r.buf)
		return nil
	}
	b := r.buf[r.off : r.off+n]
	r.off += n
	return b
}

// Raw reads n bytes. The result aliases the reader's buffer.
func (r *Reader) Raw(n int) []byte { return r.take(n) }

// Rest reads everything left.
func (r *Reader) Rest() []byte { return r.take(r.Remaining()) }

func (r *Reader) U8() uint8 {
	b := r.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (r *Reader) U16() uint16 {
	b := r.take(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

func (r *Reader) U32() uint32 {
	b := r.take(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (r *Reader) U64() uint64 {
	b := r.take(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

func (r *Reader) S8() int8           { return int8(r.U8()) }
func (r *Reader) S16() int16         { return int16(r.U16()) }
func (r *Reader) S32() int32         { return int32(r.U32()) }
func (r *Reader) S64() int64         { return int64(r.U64()) }
func (r *Reader) F64() float64       { return math.Float64frombits(r.U64()) }
func (r *Reader) PID() uint64        { return r.U64() }
func (r *Reader) Result() uint32     { return r.U32() }
func (r *Reader) DateTime() DateTime { return DateTime(r.U64()) }
func (r *Reader) Bool() bool         { return r.U8() != 0 }

// String reads a NEX string. A length of 0 (no terminator at all) reads as "".
func (r *Reader) String() string {
	n := int(r.U16())
	if n == 0 {
		return ""
	}
	b := r.take(n)
	if b == nil {
		return ""
	}
	if b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}

// Buffer reads a u32-length buffer. The result is a copy.
func (r *Reader) Buffer() []byte {
	n := r.U32()
	if uint64(n) > uint64(r.Remaining()) {
		r.Fail(ErrShortRead)
		return nil
	}
	return append([]byte(nil), r.take(int(n))...)
}

// QBuffer reads a u16-length buffer. The result is a copy.
func (r *Reader) QBuffer() []byte {
	n := int(r.U16())
	return append([]byte(nil), r.take(n)...)
}

// StationURL reads a station URL. A malformed one is recorded as the reader's
// error: every place a station is read, the client is describing where it can
// be reached, and a half-parsed address is worse than refusing the call.
func (r *Reader) StationURL() *StationURL {
	s := r.String()
	u, err := ParseStationURL(s)
	if err != nil {
		r.Fail(err)
		return NewStationURL("prudp")
	}
	return u
}

// Struct reads one structure level and hands its content to fn as its own
// reader. Content the level carries beyond what fn reads is skipped, which is
// the point of the header: a newer console may append fields to a structure
// without bumping its version, and those must not shift the next value.
func (r *Reader) Struct(fn func(sub *Reader, version uint8)) {
	version := r.U8()
	content := r.Buffer()
	if r.err != nil {
		return
	}
	sub := NewReader(content)
	fn(sub, version)
	if sub.err != nil {
		r.Fail(sub.err)
	}
}

// AnyData reads an AnyDataHolder, returning its type name and a reader over
// the object.
func (r *Reader) AnyData() (string, *Reader) {
	name := r.String()
	outer := r.U32()
	inner := r.Buffer()
	if r.err == nil && int(outer) != len(inner)+4 {
		r.Fail(fmt.Errorf("nex: AnyDataHolder lengths disagree (%d vs %d+4)", outer, len(inner)))
	}
	return name, NewReader(inner)
}

// Variant reads a Variant.
func (r *Reader) Variant() Variant {
	v := Variant{Type: VariantType(r.U8())}
	switch v.Type {
	case VariantNone:
	case VariantInt64:
		v.Int = r.S64()
	case VariantDouble:
		v.Double = r.F64()
	case VariantBool:
		v.Bool = r.Bool()
	case VariantString:
		v.String = r.String()
	case VariantDateTime:
		v.DateTime = r.DateTime()
	case VariantUInt64:
		v.UInt = r.U64()
	default:
		r.Fail(fmt.Errorf("nex: variant has unknown type %d", v.Type))
	}
	return v
}

// maxListLength bounds a list count read off the wire. A console sends lists
// of a few dozen at most; without a bound, a hostile u32 count would size an
// allocation before the stream ran out.
const maxListLength = 1 << 16

// ReadList reads a u32 count and that many elements.
func ReadList[T any](r *Reader, each func(*Reader) T) []T {
	n := r.U32()
	if r.err != nil {
		return nil
	}
	// Every element is at least one byte, so a count larger than what is left
	// cannot be honest.
	if n > maxListLength || int(n) > r.Remaining() {
		r.Fail(fmt.Errorf("nex: list count %d exceeds the %d bytes left", n, r.Remaining()))
		return nil
	}
	out := make([]T, 0, n)
	for i := uint32(0); i < n && r.err == nil; i++ {
		out = append(out, each(r))
	}
	return out
}

// DateTime is NEX's packed date and time: year in bits 63-26, then month,
// day, hour, minute and second. Always UTC on the wire.
type DateTime uint64

// NewDateTime packs t (converted to UTC).
func NewDateTime(t time.Time) DateTime {
	t = t.UTC()
	return DateTime(uint64(t.Second()) |
		uint64(t.Minute())<<6 |
		uint64(t.Hour())<<12 |
		uint64(t.Day())<<17 |
		uint64(t.Month())<<22 |
		uint64(t.Year())<<26)
}

// Now is the current time as a DateTime.
func Now() DateTime { return NewDateTime(time.Now()) }

// Time unpacks d. The zero DateTime ("never") returns the zero time.
func (d DateTime) Time() time.Time {
	if d == 0 {
		return time.Time{}
	}
	v := uint64(d)
	return time.Date(int(v>>26), time.Month((v>>22)&0xF), int((v>>17)&0x1F),
		int((v>>12)&0x1F), int((v>>6)&0x3F), int(v&0x3F), 0, time.UTC)
}

// VariantType is a Variant's type id.
type VariantType uint8

const (
	VariantNone     VariantType = 0
	VariantInt64    VariantType = 1
	VariantDouble   VariantType = 2
	VariantBool     VariantType = 3
	VariantString   VariantType = 4
	VariantDateTime VariantType = 5
	VariantUInt64   VariantType = 6
)

// Variant is a tagged value. Only the field matching Type is meaningful.
type Variant struct {
	Type     VariantType
	Int      int64
	Double   float64
	Bool     bool
	String   string
	DateTime DateTime
	UInt     uint64
}
