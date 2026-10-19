package stunresponder

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

// buildBindingRequest mirrors the shape the console sends: type Binding
// Request, zero-length body, the RFC 5389 magic cookie, and a 12-byte
// transaction id.
func buildBindingRequest(transactionID [12]byte) []byte {
	out := make([]byte, 0, headerSize)
	out = binary.BigEndian.AppendUint16(out, typeBindingRequest)
	out = binary.BigEndian.AppendUint16(out, 0)
	out = binary.BigEndian.AppendUint32(out, magicCookie)
	out = append(out, transactionID[:]...)
	return out
}

// TestBindingResponseEchoesTheObservedAddress is the core of what this
// package exists for: a console must be able to recover its own public
// address from the response, XORed with the magic cookie per RFC 5389 15.2.
func TestBindingResponseEchoesTheObservedAddress(t *testing.T) {
	var tid [12]byte
	copy(tid[:], []byte{1, 2, 3, 4, 5, 6, 7, 8, 0x14, 0x1d, 0x73, 0xb5})
	req := buildBindingRequest(tid)

	from := &net.UDPAddr{IP: net.IPv4(81, 242, 84, 26), Port: 40391}
	resp, ok := bindingResponse(req, from)
	if !ok {
		t.Fatal("a well-formed Binding Request was not answered")
	}

	if got := binary.BigEndian.Uint16(resp[0:2]); got != typeBindingResponse {
		t.Errorf("message type = 0x%04x, want 0x%04x", got, typeBindingResponse)
	}
	if got := binary.BigEndian.Uint32(resp[4:8]); got != magicCookie {
		t.Errorf("magic cookie = 0x%08x, want 0x%08x", got, magicCookie)
	}
	if !bytes.Equal(resp[8:headerSize], tid[:]) {
		t.Errorf("transaction id = % x, want % x (echoed from the request)", resp[8:headerSize], tid)
	}

	attrType := binary.BigEndian.Uint16(resp[headerSize : headerSize+2])
	if attrType != attrXorMappedAddress {
		t.Fatalf("attribute type = 0x%04x, want XOR-MAPPED-ADDRESS", attrType)
	}
	value := resp[headerSize+4 : headerSize+4+8]
	if value[1] != familyIPv4 {
		t.Errorf("family = %d, want IPv4", value[1])
	}

	xport := binary.BigEndian.Uint16(value[2:4])
	gotPort := xport ^ uint16(magicCookie>>16)
	if int(gotPort) != from.Port {
		t.Errorf("recovered port = %d, want %d", gotPort, from.Port)
	}

	var cookieBytes [4]byte
	binary.BigEndian.PutUint32(cookieBytes[:], magicCookie)
	gotIP := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		gotIP[i] = value[4+i] ^ cookieBytes[i]
	}
	if !gotIP.Equal(from.IP) {
		t.Errorf("recovered address = %v, want %v", gotIP, from.IP)
	}
}

// TestNonSTUNTrafficIsIgnored proves the responder does not answer garbage
// on the port with something that might confuse an unrelated sender -- the
// same tolerance internal/natcheck's own responder has for traffic that is
// not its protocol.
func TestNonSTUNTrafficIsIgnored(t *testing.T) {
	from := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 12345}
	cases := [][]byte{
		nil,
		[]byte("not stun"),
		buildBindingRequest([12]byte{})[:10], // truncated
	}
	for _, req := range cases {
		if _, ok := bindingResponse(req, from); ok {
			t.Errorf("non-STUN input %q was answered", req)
		}
	}

	// Right length, wrong magic cookie: must not be mistaken for STUN.
	req := buildBindingRequest([12]byte{})
	binary.BigEndian.PutUint32(req[4:8], 0)
	if _, ok := bindingResponse(req, from); ok {
		t.Error("a request with the wrong magic cookie was answered")
	}
}

// TestServeRoundTrips is the end-to-end path: a real UDP client sends a
// Binding Request and gets back a response it can parse the same way
// TestBindingResponseEchoesTheObservedAddress does.
func TestServeRoundTrips(t *testing.T) {
	conn, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := conn.LocalAddr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	errCh := make(chan error, 1)
	go func() { errCh <- Serve(ctx, conn, discard) }()

	client, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	req := buildBindingRequest([12]byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9})
	if _, err := client.Write(req); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	buf := make([]byte, 1500)
	if _, err := client.Read(buf); err != nil {
		t.Fatalf("no response received: %v", err)
	}
	if got := binary.BigEndian.Uint16(buf[0:2]); got != typeBindingResponse {
		t.Errorf("message type = 0x%04x, want 0x%04x", got, typeBindingResponse)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Errorf("Serve: %v", err)
	}
}
