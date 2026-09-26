package nex

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"
)

func TestAnEmptyStringIsWrittenWithItsTerminator(t *testing.T) {
	w := NewWriter()
	w.String("")
	w.String("ab")
	if got, want := hex.EncodeToString(w.Bytes()), "010000"+"0300616200"; got != want {
		t.Fatalf("wire = %s, want %s", got, want)
	}
	r := NewReader(w.Bytes())
	if a, b := r.String(), r.String(); a != "" || b != "ab" || r.Err() != nil {
		t.Fatalf("read back %q %q err=%v", a, b, r.Err())
	}
}

func TestAZeroLengthStringReadsAsEmpty(t *testing.T) {
	r := NewReader([]byte{0, 0, 7})
	if s := r.String(); s != "" || r.Err() != nil || r.U8() != 7 {
		t.Fatalf("got %q, err=%v", s, r.Err())
	}
}

func TestAStructureCarriesAVersionAndLengthPerLevel(t *testing.T) {
	// Data (an empty parent) then a child with one u32: this is how
	// AuthenticationInfo arrives inside LoginEx.
	w := NewWriter()
	w.Struct(0, nil)
	w.Struct(1, func(w *Writer) { w.U32(0xAABBCCDD) })
	want := "00" + "00000000" + "01" + "04000000" + "ddccbbaa"
	if got := hex.EncodeToString(w.Bytes()); got != want {
		t.Fatalf("wire = %s, want %s", got, want)
	}
}

func TestAStructureLevelSkipsFieldsItDoesNotKnow(t *testing.T) {
	// A newer console appending a field without bumping the version must not
	// shift the value after the structure.
	w := NewWriter()
	w.Struct(0, func(w *Writer) { w.U32(1); w.U32(99) })
	w.U8(0x42)

	r := NewReader(w.Bytes())
	var got uint32
	r.Struct(func(sub *Reader, _ uint8) { got = sub.U32() })
	if got != 1 || r.U8() != 0x42 || r.Err() != nil {
		t.Fatalf("got=%d err=%v", got, r.Err())
	}
}

func TestAnyDataHolderOuterLengthCountsTheInnerLength(t *testing.T) {
	w := NewWriter()
	w.AnyData("X", func(w *Writer) { w.U8(5) })
	// "X": 0200 5800 ; outer 5 ; inner 1 ; 05
	if got, want := hex.EncodeToString(w.Bytes()), "02005800"+"05000000"+"01000000"+"05"; got != want {
		t.Fatalf("wire = %s, want %s", got, want)
	}
	name, sub := NewReader(w.Bytes()).AnyData()
	if name != "X" || sub.U8() != 5 {
		t.Fatalf("name=%q", name)
	}
}

func TestAListCountLargerThanTheStreamIsRefused(t *testing.T) {
	r := NewReader([]byte{0xFF, 0xFF, 0xFF, 0x7F, 1})
	if items := ReadList(r, (*Reader).U8); items != nil || r.Err() == nil {
		t.Fatalf("items=%v err=%v", items, r.Err())
	}
}

func TestAShortReadIsSticky(t *testing.T) {
	r := NewReader([]byte{1, 2})
	_ = r.U32()
	if r.U8() != 0 || r.Err() != ErrShortRead {
		t.Fatalf("err = %v", r.Err())
	}
}

func TestDateTimeRoundTripsThroughItsBitFields(t *testing.T) {
	at := time.Date(2026, 9, 26, 13, 45, 7, 0, time.UTC)
	d := NewDateTime(at)
	// year<<26 | month<<22 | day<<17 | hour<<12 | minute<<6 | second
	want := uint64(2026)<<26 | 9<<22 | 26<<17 | 13<<12 | 45<<6 | 7
	if uint64(d) != want {
		t.Fatalf("packed %x, want %x", uint64(d), want)
	}
	if !d.Time().Equal(at) {
		t.Fatalf("unpacked %v", d.Time())
	}
}

func TestVariantRoundTrips(t *testing.T) {
	for _, v := range []Variant{
		{Type: VariantNone},
		{Type: VariantInt64, Int: -3},
		{Type: VariantBool, Bool: true},
		{Type: VariantString, String: "hi"},
		{Type: VariantUInt64, UInt: 7},
	} {
		w := NewWriter()
		w.Variant(v)
		if got := NewReader(w.Bytes()).Variant(); got != v {
			t.Errorf("%+v read back as %+v", v, got)
		}
	}
	r := NewReader([]byte{9})
	r.Variant()
	if r.Err() == nil {
		t.Fatal("an unknown variant type was accepted")
	}
}

func TestStationURLKeepsParameterOrder(t *testing.T) {
	const s = "prudps:/address=10.0.0.2;port=5;PID=2;sid=1"
	u, err := ParseStationURL(s)
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != s {
		t.Fatalf("re-emitted as %q", u.String())
	}
	u.SetInt("port", 6)
	u.Set("type", "3")
	u.Delete("PID")
	if got := u.String(); got != "prudps:/address=10.0.0.2;port=6;sid=1;type=3" {
		t.Fatalf("got %q", got)
	}
	if u.Int("port") != 6 || u.Get("missing") != "" {
		t.Fatal("accessors")
	}
}

func TestAMalformedStationURLFailsTheReader(t *testing.T) {
	w := NewWriter()
	w.String("no-scheme-here")
	r := NewReader(w.Bytes())
	r.StationURL()
	if r.Err() == nil {
		t.Fatal("accepted a station without a scheme")
	}
}

func TestBuffersAreCopies(t *testing.T) {
	w := NewWriter()
	w.Buffer([]byte{1, 2})
	raw := w.Bytes()
	b := NewReader(raw).Buffer()
	raw[4] = 9
	if !bytes.Equal(b, []byte{1, 2}) {
		t.Fatal("buffer aliases the stream")
	}
}
