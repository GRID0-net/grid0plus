package nex

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestAPacketHeaderIsLaidOutAsTheLiteFormat(t *testing.T) {
	p := &Packet{SourceType: 10, DestType: 10, SourcePort: 1, DestPort: 0x1F,
		Type: PacketDATA, Flags: FlagReliable | FlagNeedACK | FlagHasSize, Sequence: 0x0102,
		FragmentID: 3, Payload: []byte{0xEE}}
	// 80 | 00 opts | 0100 payload | aa types | 01 1f ports | 03 frag |
	// (flags 0x00e << 4 | type 2) = 0x00e2 | 0201 seq | ee
	if got, want := hex.EncodeToString(p.Encode()), "80000100aa011f03e2000201ee"; got != want {
		t.Fatalf("wire %s, want %s", got, want)
	}
}

// Kinnay's client checks the option SET on everything it receives and drops
// the packet if it differs. These are the sets it expects.
func TestEachPacketCarriesExactlyTheOptionsAClientExpects(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Packet
		want string
	}{
		{"SYN/ACK", Packet{Type: PacketSYN, Flags: FlagACK, MinorVersion: 3,
			ConnectionSignature: bytes.Repeat([]byte{0xAB}, 16)},
			"000403000000" + "0110" + "abababababababababababababababab"},
		{"CONNECT/ACK", Packet{Type: PacketCONNECT, Flags: FlagACK | FlagHasSize, MinorVersion: 3},
			"000403000000"},
		{"DATA", Packet{Type: PacketDATA, Flags: FlagACK}, ""},
		{"PING", Packet{Type: PacketPING, Flags: FlagACK}, ""},
	} {
		enc := tc.p.Encode()
		opts := enc[liteHeaderSize : liteHeaderSize+int(enc[1])]
		if got := hex.EncodeToString(opts); got != tc.want {
			t.Errorf("%s options %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestPacketsRoundTripAndAPartialOneIsKept(t *testing.T) {
	a := (&Packet{Type: PacketCONNECT, SourcePort: 0x1F, DestPort: 2, Sequence: 1, MinorVersion: 2,
		LiteSignature: bytes.Repeat([]byte{7}, 16), Payload: []byte("ticket")}).Encode()
	b := (&Packet{Type: PacketDATA, Flags: FlagReliable, Sequence: 2, Payload: []byte("x")}).Encode()
	buf := append(append([]byte{}, a...), b[:5]...)

	pkts, rest, err := DecodePackets(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkts) != 1 || !bytes.Equal(rest, b[:5]) {
		t.Fatalf("got %d packets, rest %x", len(pkts), rest)
	}
	p := pkts[0]
	if p.Type != PacketCONNECT || p.DestPort != 2 || p.MinorVersion != 2 ||
		!bytes.Equal(p.LiteSignature, bytes.Repeat([]byte{7}, 16)) || string(p.Payload) != "ticket" {
		t.Fatalf("decoded %+v", p)
	}
}

func TestSomethingThatIsNotPRUDPLiteIsRejected(t *testing.T) {
	if _, _, err := DecodePackets(bytes.Repeat([]byte{0xEA}, 20)); err != ErrBadMagic {
		t.Fatalf("err = %v", err)
	}
}

func TestTheLiteSignatureIsKeyedWithTheAccessKey(t *testing.T) {
	sig := bytes.Repeat([]byte{1}, 16)
	if bytes.Equal(liteSignature("aaaa", sig), liteSignature("bbbb", sig)) {
		t.Fatal("the signature does not depend on the access key")
	}
	if bytes.Equal(liteSignature("aaaa", sig), liteSignature("aaaa", bytes.Repeat([]byte{2}, 16))) {
		t.Fatal("the signature does not depend on the connection signature")
	}
}

func TestSequenceComparisonWraps(t *testing.T) {
	if !seqBefore(0xFFFF, 0) || seqBefore(0, 0xFFFF) || seqBefore(5, 5) {
		t.Fatal("wrapping comparison")
	}
}
