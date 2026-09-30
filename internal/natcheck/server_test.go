package natcheck

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestDecodeAndEncodeRoundTrip(t *testing.T) {
	want := Message{
		Type:         TypeReachability,
		ExternalPort: 51234,
		ExternalIP:   0xC0A80132, // 192.168.1.50
		LocalIP:      0x0A000001, // 10.0.0.1
	}

	got, err := Decode(want.Encode())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestEncodeIsBigEndian(t *testing.T) {
	// The protocol is big-endian. Getting this wrong makes the console read its
	// own external address byte-reversed, which it then reports as an
	// unreachable NAT rather than as a protocol error.
	buf := Message{Type: 1, ExternalPort: 0x1234, ExternalIP: 0x0A0B0C0D}.Encode()

	if len(buf) != 16 {
		t.Fatalf("message is %d bytes, want 16", len(buf))
	}
	if binary.BigEndian.Uint32(buf[0:4]) != 1 {
		t.Error("type is not big-endian")
	}
	if buf[6] != 0x12 || buf[7] != 0x34 {
		t.Errorf("port bytes = %#x %#x, want big-endian 0x12 0x34", buf[6], buf[7])
	}
	if buf[8] != 0x0A || buf[11] != 0x0D {
		t.Errorf("IP bytes = %#x..%#x, want big-endian", buf[8], buf[11])
	}
}

func TestDecodeRejectsWrongLengths(t *testing.T) {
	// A short or long datagram means something other than a console is talking
	// to the port. Padding or truncating it would turn noise into a reply.
	for _, n := range []int{0, 1, 15, 17, 64} {
		if _, err := Decode(make([]byte, n)); err == nil {
			t.Errorf("accepted a %d-byte datagram", n)
		}
	}
}

// listenTestServer starts a responder on loopback with ephemeral ports.
func listenTestServer(t *testing.T, withAlternate bool) *Server {
	t.Helper()

	cfg := Config{
		PrimaryListen:   "127.0.0.1:0",
		SecondaryListen: "127.0.0.1:0",
		AdvertisedIP:    "127.0.0.1",
	}
	if withAlternate {
		cfg.AlternateListen = "127.0.0.2:0"
		cfg.AlternateAdvertisedIP = "127.0.0.2"
	}

	srv, err := Listen(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not shut down")
		}
	})
	return srv
}

// probe sends one message and returns the reply plus the address it came from.
func probe(t *testing.T, target string, msg Message) (Message, *net.UDPAddr) {
	t.Helper()

	addr, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.WriteToUDP(msg.Encode(), addr); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 64)
	n, from, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("no reply to message type %d: %v", msg.Type, err)
	}
	reply, err := Decode(buf[:n])
	if err != nil {
		t.Fatalf("reply does not decode: %v", err)
	}
	return reply, from
}

// TestServerReportsTheClientsExternalAddress is the whole point of the protocol:
// the console learns the mapping its router created.
func TestServerReportsTheClientsExternalAddress(t *testing.T) {
	srv := listenTestServer(t, true)
	primary := srv.Addrs()[0]

	local, err := net.ResolveUDPAddr("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp4", local)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	target, err := net.ResolveUDPAddr("udp4", primary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.WriteToUDP(Message{Type: TypeReachability}.Encode(), target); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 64)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("no reply: %v", err)
	}
	reply, err := Decode(buf[:n])
	if err != nil {
		t.Fatal(err)
	}

	wantPort := uint32(conn.LocalAddr().(*net.UDPAddr).Port)
	if reply.ExternalPort != wantPort {
		t.Errorf("external port = %d, want the source port %d", reply.ExternalPort, wantPort)
	}
	if reply.ExternalIP != 0x7F000001 {
		t.Errorf("external IP = %#x, want 127.0.0.1", reply.ExternalIP)
	}
	if reply.Type != TypeReachability {
		t.Errorf("reply type = %d, want the request's type echoed", reply.Type)
	}
	// The last field is the server's own address, which the console compares
	// against to detect a NAT at all.
	if reply.LocalIP != 0x7F000001 {
		t.Errorf("local IP = %#x, want the configured 127.0.0.1", reply.LocalIP)
	}
}

// TestSourcePortPerMessageType is what NAT classification is built on: the
// console decides its NAT type from WHICH socket answered.
func TestSourcePortPerMessageType(t *testing.T) {
	srv := listenTestServer(t, true)
	addrs := srv.Addrs()
	if len(addrs) != 3 {
		t.Fatalf("got %d sockets, want primary, secondary and alternate", len(addrs))
	}
	primary, secondary, alternate := addrs[0], addrs[1], addrs[2]

	port := func(addr string) string {
		_, p, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	cases := []struct {
		msgType  uint32
		wantPort string
		why      string
	}{
		{TypeReachability, port(primary), "reachability is answered from the regular socket"},
		{TypeDifferentAddress, port(alternate), "type 2 must come from a different address"},
		{TypeDifferentPort, port(secondary), "type 3 must come from a different port"},
		{TypeMapping1, port(primary), "mapping probes come from the regular socket"},
		{TypeMapping2, port(primary), "mapping probes come from the regular socket"},
		{TypeReachabilityAlt, port(primary), "101 behaves like 1"},
		{TypeDifferentPortAlt, port(secondary), "102 behaves like 3"},
		{TypeReachabilityAlt2, port(primary), "103 behaves like 1"},
	}
	for _, c := range cases {
		reply, from := probe(t, primary, Message{Type: c.msgType})
		if got := itoa(from.Port); got != c.wantPort {
			t.Errorf("type %d: reply came from port %s, want %s (%s)",
				c.msgType, got, c.wantPort, c.why)
		}
		if c.msgType == TypeDifferentAddress && reply.LocalIP != ipToUint32(net.IPv4(127, 0, 0, 2)) {
			t.Errorf("type 2 advertised local IP %#x, want the alternate public address", reply.LocalIP)
		}
	}
}

// Both NCS hostnames and the secondary port accept reachability probes.
// Their replies must come from the socket the client actually addressed.
func TestReachabilityRepliesFromTheAddressedSocket(t *testing.T) {
	srv := listenTestServer(t, true)
	for _, target := range srv.Addrs() {
		reply, from := probe(t, target, Message{Type: TypeReachability})
		want, err := net.ResolveUDPAddr("udp4", target)
		if err != nil {
			t.Fatal(err)
		}
		if from.Port != want.Port || !from.IP.Equal(want.IP) {
			t.Errorf("probe to %s replied from %s", target, from)
		}
		if reply.LocalIP != ipToUint32(want.IP) {
			t.Errorf("probe to %s advertised local IP %#x", target, reply.LocalIP)
		}
	}
}

// TestWithoutAnAlternateAddressTypeTwoStillAnswers covers the single-address
// deployment. It is degraded — the console classifies its NAT as stricter than
// it is — but detection has to terminate rather than hang.
func TestWithoutAnAlternateAddressTypeTwoStillAnswers(t *testing.T) {
	srv := listenTestServer(t, false)
	addrs := srv.Addrs()
	if len(addrs) != 2 {
		t.Fatalf("got %d sockets, want primary and secondary only", len(addrs))
	}

	reply, from := probe(t, addrs[0], Message{Type: TypeDifferentAddress})
	if reply.Type != TypeDifferentAddress {
		t.Errorf("reply type = %d", reply.Type)
	}
	_, secondaryPort, err := net.SplitHostPort(addrs[1])
	if err != nil {
		t.Fatal(err)
	}
	if itoa(from.Port) != secondaryPort {
		t.Errorf("type 2 was answered from port %d, want the secondary port %s "+
			"as the documented fallback", from.Port, secondaryPort)
	}
}

func TestUnknownMessageTypeIsAnsweredFromThePrimarySocket(t *testing.T) {
	srv := listenTestServer(t, true)

	// A firmware that adds a type we have not seen gets a reply of the right
	// shape rather than silence, and the type is logged so it can be looked into.
	reply, _ := probe(t, srv.Addrs()[0], Message{Type: 250})
	if reply.Type != 250 {
		t.Errorf("reply type = %d, want the unknown type echoed", reply.Type)
	}
}

func TestMalformedDatagramIsIgnored(t *testing.T) {
	srv := listenTestServer(t, true)

	addr, err := net.ResolveUDPAddr("udp4", srv.Addrs()[0])
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	// Nothing should come back, and crucially the server must keep serving.
	if _, err := conn.WriteToUDP([]byte("junk"), addr); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	if _, _, err := conn.ReadFromUDP(buf); err == nil {
		t.Error("a malformed datagram got a reply")
	}

	// Still alive.
	if reply, _ := probe(t, srv.Addrs()[0], Message{Type: TypeReachability}); reply.Type != TypeReachability {
		t.Error("the server stopped serving after a malformed datagram")
	}
}

func TestListenRejectsANonIPv4AdvertisedAddress(t *testing.T) {
	_, err := Listen(Config{
		PrimaryListen: "127.0.0.1:0",
		AdvertisedIP:  "not-an-ip",
	}, slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("accepted a non-IP advertised address")
	}
}

// itoa avoids pulling strconv into the assertions.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
