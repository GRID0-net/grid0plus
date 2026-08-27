package dns

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

// A stub upstream, so the forwarding path can be tested without the internet.
type stubUpstream struct {
	conn *net.UDPConn
	// answerIP is what it answers every A query with. Deliberately a public
	// address, so a test can tell "the query was forwarded and got Nintendo's
	// real address" apart from "the query was redirected to us".
	answerIP net.IP
	queries  chan string
}

func newStubUpstream(t *testing.T, answerIP net.IP) *stubUpstream {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	u := &stubUpstream{conn: conn, answerIP: answerIP, queries: make(chan string, 64)}

	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			query := make([]byte, n)
			copy(query, buf[:n])

			h, q, err := parseQuery(query)
			if err != nil {
				continue
			}
			select {
			case u.queries <- q.Name + "/" + typeName(q.Type):
			default:
			}

			var reply []byte
			if wantsAddress(q.Type) {
				reply = answer(query, h, q, u.answerIP, 300)
			} else {
				reply = answer(query, h, q, nil, 300)
			}
			_, _ = conn.WriteToUDP(reply, from)
		}
	}()

	t.Cleanup(func() { _ = conn.Close() })
	return u
}

func (u *stubUpstream) addr() string { return u.conn.LocalAddr().String() }

func (u *stubUpstream) sawQuery(name string) bool {
	for {
		select {
		case got := <-u.queries:
			if strings.HasPrefix(got, name+"/") {
				return true
			}
		default:
			return false
		}
	}
}

type harness struct {
	t        *testing.T
	server   *Server
	upstream *stubUpstream
	resolver *net.Resolver
}

func newHarness(t *testing.T, tweak ...func(*Config)) *harness {
	t.Helper()

	upstream := newStubUpstream(t, net.IPv4(203, 0, 113, 99))

	cfg := Config{
		Listen:            "127.0.0.1:0",
		RedirectTo:        net.IPv4(192, 168, 1, 50),
		NATCheckSecondary: net.IPv4(192, 168, 1, 51),
		Upstream:          []string{upstream.addr()},
		TTL:               60,
		UpstreamTimeout:   2 * time.Second,
	}
	for _, f := range tweak {
		f(&cfg)
	}

	srv, err := Listen(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not shut down")
		}
	})

	addr := srv.Addr()

	// Go's own stub resolver, pointed at our server. Testing through an
	// independent DNS implementation is the point: it catches wire-format
	// mistakes that a test using this package's own encoder would agree with.
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}

	return &harness{t: t, server: srv, upstream: upstream, resolver: resolver}
}

// query sends a raw query and returns the raw reply, so tests can assert on the
// wire rather than through a client library.
func (h *harness) query(name string, qtype uint16, overTCP bool) []byte {
	h.t.Helper()

	msg := buildQuery(0x1234, name, qtype)

	if overTCP {
		conn, err := net.DialTimeout("tcp", h.server.Addr(), 3*time.Second)
		if err != nil {
			h.t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			h.t.Fatal(err)
		}
		if err := writeTCPMessage(conn, msg); err != nil {
			h.t.Fatal(err)
		}
		reply, err := readTCPMessage(conn)
		if err != nil {
			h.t.Fatal(err)
		}
		return reply
	}

	conn, err := net.DialTimeout("udp", h.server.Addr(), 3*time.Second)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		h.t.Fatal(err)
	}
	if _, err := conn.Write(msg); err != nil {
		h.t.Fatal(err)
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		h.t.Fatal(err)
	}
	return buf[:n]
}

// buildQuery encodes a query. Written independently of message.go's encoder so a
// bug in one does not hide a bug in the other.
func buildQuery(id uint16, name string, qtype uint16) []byte {
	msg := make([]byte, headerLen)
	binary.BigEndian.PutUint16(msg[0:2], id)
	binary.BigEndian.PutUint16(msg[2:4], 0x0100) // RD
	binary.BigEndian.PutUint16(msg[4:6], 1)      // qdcount

	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	msg = binary.BigEndian.AppendUint16(msg, classIN)
	return msg
}

// parsedReply is a decoded response, for assertions.
type parsedReply struct {
	id      uint16
	rcode   uint16
	qr      bool
	aa      bool
	tc      bool
	answers []net.IP
	ancount uint16
}

func parseReply(t *testing.T, msg []byte) parsedReply {
	t.Helper()
	if len(msg) < headerLen {
		t.Fatalf("reply is %d bytes", len(msg))
	}

	flags := binary.BigEndian.Uint16(msg[2:4])
	r := parsedReply{
		id:      binary.BigEndian.Uint16(msg[0:2]),
		rcode:   flags & 0xf,
		qr:      flags&0x8000 != 0,
		aa:      flags&0x0400 != 0,
		tc:      flags&0x0200 != 0,
		ancount: binary.BigEndian.Uint16(msg[6:8]),
	}

	// Skip the question section.
	offset := headerLen
	for offset < len(msg) && msg[offset] != 0 {
		offset += int(msg[offset]) + 1
	}
	offset += 1 + 4

	for range r.ancount {
		if offset+2 > len(msg) {
			t.Fatal("answer runs past the message")
		}
		// The name: either a compression pointer or labels.
		if msg[offset]&0xc0 == 0xc0 {
			offset += 2
		} else {
			for offset < len(msg) && msg[offset] != 0 {
				offset += int(msg[offset]) + 1
			}
			offset++
		}
		if offset+10 > len(msg) {
			t.Fatal("answer header runs past the message")
		}
		rrType := binary.BigEndian.Uint16(msg[offset : offset+2])
		rdLength := int(binary.BigEndian.Uint16(msg[offset+8 : offset+10]))
		offset += 10
		if offset+rdLength > len(msg) {
			t.Fatal("rdata runs past the message")
		}
		if rrType == TypeA && rdLength == 4 {
			r.answers = append(r.answers, net.IP(msg[offset:offset+4]).To4())
		}
		offset += rdLength
	}
	return r
}

// ---- redirection ---------------------------------------------------------

// TestNintendoHostsAreRedirected is the whole point of this server: a console
// pointed at it reaches SwitchNet without any file on its SD card.
func TestNintendoHostsAreRedirected(t *testing.T) {
	h := newHarness(t)

	for _, name := range []string{
		"dauth-lp1.ndas.srv.nintendo.net",
		"dcert-lp1.ndas.srv.nintendo.net",
		"aauth-lp1.ndas.srv.nintendo.net",
		"e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
		"m-lp1.baas.nintendo.com",
		"cdn-image-e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com",
		"accounts.nintendo.com",
		"nncs1-lp1.n.n.srv.nintendo.net",
		"api-lp1.frs.srv.nintendo.net",
		"ctest.cdn.nintendo.net",
	} {
		reply := parseReply(t, h.query(name, TypeA, false))
		if reply.rcode != rcodeNoError {
			t.Errorf("%s: rcode = %d, want 0", name, reply.rcode)
			continue
		}
		if len(reply.answers) != 1 {
			t.Errorf("%s: got %d answers, want 1", name, len(reply.answers))
			continue
		}
		if !reply.answers[0].Equal(net.IPv4(192, 168, 1, 50)) {
			t.Errorf("%s: resolved to %s, want the SwitchNet address",
				name, reply.answers[0])
		}
		if !reply.qr || !reply.aa {
			t.Errorf("%s: QR=%v AA=%v, want both set", name, reply.qr, reply.aa)
		}
	}
}

// TestNATCheckSecondaryGetsTheOtherAddress is the property Pia depends on.
//
// nncs1 and nncs2 have to be different addresses. If they are the same the
// console de-duplicates its second NAT probe, never finishes NAT detection, and
// matchmaking finds matches that will not start.
func TestNATCheckSecondaryGetsTheOtherAddress(t *testing.T) {
	h := newHarness(t)

	primary := parseReply(t, h.query("nncs1-lp1.n.n.srv.nintendo.net", TypeA, false))
	secondary := parseReply(t, h.query("nncs2-lp1.n.n.srv.nintendo.net", TypeA, false))

	if len(primary.answers) != 1 || len(secondary.answers) != 1 {
		t.Fatalf("got %d and %d answers, want 1 each",
			len(primary.answers), len(secondary.answers))
	}
	if primary.answers[0].Equal(secondary.answers[0]) {
		t.Errorf("nncs1 and nncs2 both resolved to %s; they must differ or the "+
			"console never sends its second NAT probe", primary.answers[0])
	}
	if !secondary.answers[0].Equal(net.IPv4(192, 168, 1, 51)) {
		t.Errorf("nncs2 resolved to %s, want the secondary address",
			secondary.answers[0])
	}
}

// TestAAAAIsNodataForRedirectedNames is the subtle one.
//
// If an AAAA query for a redirected name were forwarded, the console would get
// Nintendo's real IPv6 address and talk straight past SwitchNet — intermittently,
// depending on whether its network has IPv6. That failure would be very hard to
// diagnose from the console side.
func TestAAAAIsNodataForRedirectedNames(t *testing.T) {
	h := newHarness(t)

	for _, qtype := range []uint16{TypeAAAA, TypeHTTPS} {
		reply := parseReply(t, h.query("dauth-lp1.ndas.srv.nintendo.net", qtype, false))

		// NOERROR with no answers: the name exists, this type does not.
		if reply.rcode != rcodeNoError {
			t.Errorf("%s: rcode = %d, want 0 (NODATA, not an error)",
				typeName(qtype), reply.rcode)
		}
		if reply.ancount != 0 {
			t.Errorf("%s: got %d answers, want 0", typeName(qtype), reply.ancount)
		}
		if h.upstream.sawQuery("dauth-lp1.ndas.srv.nintendo.net") {
			t.Errorf("%s: the query was forwarded upstream. A console preferring "+
				"IPv6 would then reach Nintendo directly.", typeName(qtype))
		}
	}
}

// ---- forwarding ----------------------------------------------------------

func TestNonNintendoNamesAreForwarded(t *testing.T) {
	h := newHarness(t)

	reply := parseReply(t, h.query("example.com", TypeA, false))
	if len(reply.answers) != 1 {
		t.Fatalf("got %d answers, want 1 from the upstream", len(reply.answers))
	}
	if !reply.answers[0].Equal(net.IPv4(203, 0, 113, 99)) {
		t.Errorf("resolved to %s, want the upstream's answer", reply.answers[0])
	}
	if !h.upstream.sawQuery("example.com") {
		t.Error("the upstream never saw the query")
	}
}

// TestNintendoIsNeverReachedFromThisDeployment covers every host that is
// null-routed rather than redirected or left passthrough: the eShop hosts
// nothing is built behind yet, and the d4c system-update hosts.
// accounts.nintendo.com used to be in this list too, before
// internal/baas/nalink.go gave it something to route to — see
// TestNintendoHostsAreRedirected instead.
//
// The general-purpose advice for the d4c hosts is to keep them passthrough,
// because a failed system update check makes nim store a persistent flag that
// survives switching DNS back to real Nintendo — expensive to hit on a
// console that moves back and forth. This deployment's emuMMC never does
// that: it is permanently and exclusively SwitchNet's, so there is no
// "switching back" for the flag to survive into, and passthrough would only
// mean real Nintendo traffic leaving the console for no benefit. Every host
// in this list is null-routed instead, on the same reasoning: any byte of
// contact with real Nintendo while any part of this console's trust chain has
// been altered is a byte of unnecessary risk to the account behind it, with
// nothing gained in return.
func TestNintendoIsNeverReachedFromThisDeployment(t *testing.T) {
	h := newHarness(t)

	for _, name := range []string{
		"atum.hac.lp1.d4c.nintendo.net",
		"bugyo.hac.lp1.eshop.nintendo.net",
		"atumn.hac.lp1.d4c.nintendo.net",
		"aqua.hac.lp1.d4c.nintendo.net",
		"sun.hac.lp1.d4c.nintendo.net",
	} {
		reply := parseReply(t, h.query(name, TypeA, false))
		if len(reply.answers) != 1 {
			t.Errorf("%s: got %d answers", name, len(reply.answers))
			continue
		}
		if !reply.answers[0].Equal(net.IPv4zero) {
			t.Errorf("%s: resolved to %s, want 0.0.0.0. This host has nothing "+
				"built behind it; it must never reach real Nintendo.", name, reply.answers[0])
		}
		if h.upstream.sawQuery(name) {
			t.Errorf("%s: was forwarded upstream", name)
		}
	}
}

func TestTelemetryIsBlackholed(t *testing.T) {
	h := newHarness(t)

	// The wildcard entry is 'receive-%.dg.srv.nintendo.net', so a concrete host
	// under that suffix has to match.
	reply := parseReply(t, h.query("receive-lp1.dg.srv.nintendo.net", TypeA, false))
	if len(reply.answers) != 1 {
		t.Fatalf("got %d answers, want 1", len(reply.answers))
	}
	// 0.0.0.0 rather than NXDOMAIN, so the console fails to connect immediately
	// rather than retrying the lookup.
	if !reply.answers[0].Equal(net.IPv4zero) {
		t.Errorf("resolved to %s, want 0.0.0.0", reply.answers[0])
	}
	if h.upstream.sawQuery("receive-lp1.dg.srv.nintendo.net") {
		t.Error("a blackholed name was forwarded upstream")
	}
}

func TestUpstreamFailureIsServfailNotNXDomain(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		// An address nothing is listening on.
		c.Upstream = []string{"127.0.0.1:1"}
		c.UpstreamTimeout = 300 * time.Millisecond
	})

	reply := parseReply(t, h.query("example.com", TypeA, false))
	// SERVFAIL makes the client retry. NXDOMAIN would be cached as "this name
	// does not exist", which outlives the outage.
	if reply.rcode != rcodeservFail {
		t.Errorf("rcode = %d, want SERVFAIL (%d)", reply.rcode, rcodeservFail)
	}
}

func TestSecondUpstreamIsTriedWhenTheFirstFails(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.Upstream = append([]string{"127.0.0.1:1"}, c.Upstream...)
		c.UpstreamTimeout = 300 * time.Millisecond
	})

	reply := parseReply(t, h.query("example.com", TypeA, false))
	if len(reply.answers) != 1 || !reply.answers[0].Equal(net.IPv4(203, 0, 113, 99)) {
		t.Errorf("answers = %v, want the second upstream's reply", reply.answers)
	}
}

// ---- protocol details ----------------------------------------------------

// TestGoResolverCanUseTheServer runs the whole thing through Go's own stub
// resolver, which is an independent implementation of the wire format. A
// hand-rolled encoder that only its own decoder agrees with would pass every
// other test in this file and fail here.
func TestGoResolverCanUseTheServer(t *testing.T) {
	h := newHarness(t)

	for _, network := range []string{"udp", "tcp"} {
		resolver := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, h.server.Addr())
			},
		}

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		addrs, err := resolver.LookupHost(ctx, "dauth-lp1.ndas.srv.nintendo.net")
		cancel()
		if err != nil {
			t.Errorf("%s: LookupHost: %v", network, err)
			continue
		}
		if len(addrs) != 1 || addrs[0] != "192.168.1.50" {
			t.Errorf("%s: got %v, want [192.168.1.50]", network, addrs)
		}
	}
}

func TestTCPAndUDPAgree(t *testing.T) {
	h := newHarness(t)

	overUDP := parseReply(t, h.query("dauth-lp1.ndas.srv.nintendo.net", TypeA, false))
	overTCP := parseReply(t, h.query("dauth-lp1.ndas.srv.nintendo.net", TypeA, true))

	if len(overUDP.answers) != 1 || len(overTCP.answers) != 1 {
		t.Fatalf("udp gave %d answers, tcp gave %d",
			len(overUDP.answers), len(overTCP.answers))
	}
	if !overUDP.answers[0].Equal(overTCP.answers[0]) {
		t.Errorf("udp said %s, tcp said %s", overUDP.answers[0], overTCP.answers[0])
	}
}

func TestTransactionIDIsEchoed(t *testing.T) {
	h := newHarness(t)

	msg := buildQuery(0xbeef, "dauth-lp1.ndas.srv.nintendo.net", TypeA)
	conn, err := net.DialTimeout("udp", h.server.Addr(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}

	reply := parseReply(t, buf[:n])
	// A client matches the reply to its query by id. Getting it wrong means every
	// lookup times out.
	if reply.id != 0xbeef {
		t.Errorf("id = %#x, want 0xbeef", reply.id)
	}
}

func TestNameMatchingIsCaseInsensitive(t *testing.T) {
	h := newHarness(t)

	// DNS names are case-insensitive and resolvers randomise case as an
	// anti-spoofing measure, so a case-sensitive match would fail against real
	// clients while passing a naive test.
	for _, name := range []string{
		"DAUTH-LP1.NDAS.SRV.NINTENDO.NET",
		"DaUtH-Lp1.nDaS.sRv.NiNtEnDo.NeT",
	} {
		reply := parseReply(t, h.query(name, TypeA, false))
		if len(reply.answers) != 1 {
			t.Errorf("%s: got %d answers, want 1", name, len(reply.answers))
			continue
		}
		if !reply.answers[0].Equal(net.IPv4(192, 168, 1, 50)) {
			t.Errorf("%s: resolved to %s", name, reply.answers[0])
		}
	}
}

func TestANYQueryGetsTheAddress(t *testing.T) {
	h := newHarness(t)
	reply := parseReply(t, h.query("dauth-lp1.ndas.srv.nintendo.net", TypeANY, false))
	if len(reply.answers) != 1 {
		t.Errorf("got %d answers, want 1", len(reply.answers))
	}
}

func TestMalformedQueriesDoNotCrashTheServer(t *testing.T) {
	h := newHarness(t)

	conn, err := net.DialTimeout("udp", h.server.Addr(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	cases := [][]byte{
		{},
		{0x00},
		make([]byte, headerLen),               // header only, qdcount 0
		append(make([]byte, headerLen), 0xff), // a label length past the end
		append(make([]byte, headerLen), 0xc0, 0x0c), // a compression pointer in the question
		bytes(0x41, 300), // just noise
	}
	for i, msg := range cases {
		if len(msg) == 0 {
			continue
		}
		if _, err := conn.Write(msg); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
	}

	// The server must still be answering.
	reply := parseReply(t, h.query("dauth-lp1.ndas.srv.nintendo.net", TypeA, false))
	if len(reply.answers) != 1 {
		t.Errorf("the server stopped answering after malformed input: %d answers",
			len(reply.answers))
	}
}

func bytes(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// ---- configuration -------------------------------------------------------

func TestListenRejectsTheSameAddressForBothNATCheckHosts(t *testing.T) {
	_, err := Listen(Config{
		Listen:            "127.0.0.1:0",
		RedirectTo:        net.IPv4(192, 168, 1, 50),
		NATCheckSecondary: net.IPv4(192, 168, 1, 50),
		Upstream:          []string{"127.0.0.1:53"},
	}, slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("accepted the same address for nncs1 and nncs2")
	}
}

func TestListenRequiresARedirectAddressAndAnUpstream(t *testing.T) {
	for name, cfg := range map[string]Config{
		"no redirect address": {Listen: "127.0.0.1:0", Upstream: []string{"127.0.0.1:53"}},
		"no upstream":         {Listen: "127.0.0.1:0", RedirectTo: net.IPv4(192, 168, 1, 50)},
		"ipv6 redirect": {
			Listen: "127.0.0.1:0", RedirectTo: net.ParseIP("2001:db8::1"),
			Upstream: []string{"127.0.0.1:53"},
		},
	} {
		if _, err := Listen(cfg, slog.New(slog.DiscardHandler)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
