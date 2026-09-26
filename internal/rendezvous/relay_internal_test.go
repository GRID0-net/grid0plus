package rendezvous

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestARelayPairForwardsBothWaysAndRefusesStrangers(t *testing.T) {
	s := New(Options{Title: Title{Name: "t", AccessKey: "x"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	r := &Relay{s: s, log: s.log, cfg: RelayConfig{IdleTimeout: time.Minute}, pairs: map[[2]uint64]*relayPair{}, ports: map[int]bool{}}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	p := &relayPair{port: conn.LocalAddr().(*net.UDPAddr).Port, conn: conn, ips: [2]string{"127.0.0.2", "127.0.0.3"}, last: time.Now()}
	go r.loop(p)
	defer conn.Close()

	dial := func(ip string) *net.UDPConn {
		c, err := net.DialUDP("udp4", &net.UDPAddr{IP: net.ParseIP(ip)}, conn.LocalAddr().(*net.UDPAddr))
		if err != nil {
			t.Skipf("cannot send from %s: %v", ip, err)
		}
		return c
	}
	a, b, stranger := dial("127.0.0.2"), dial("127.0.0.3"), dial("127.0.0.9")
	defer a.Close()
	defer b.Close()
	defer stranger.Close()

	read := func(c *net.UDPConn) string {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 64)
		n, err := c.Read(buf)
		if err != nil {
			return ""
		}
		return string(buf[:n])
	}
	// b must send first so the relay learns where b is.
	_, _ = b.Write([]byte("hello-from-b"))
	time.Sleep(50 * time.Millisecond)
	_, _ = a.Write([]byte("probe"))
	if got := read(b); got != "probe" {
		t.Fatalf("b received %q", got)
	}
	_, _ = b.Write([]byte("reply"))
	if got := read(a); got != "reply" {
		t.Fatalf("a received %q", got)
	}
	_, _ = stranger.Write([]byte("inject"))
	time.Sleep(50 * time.Millisecond)
	p.mu.Lock()
	rejected := p.reject
	p.mu.Unlock()
	if rejected == 0 {
		t.Fatal("a stranger's packet was not refused")
	}
}

func TestSameNet24(t *testing.T) {
	if !sameNet24("100.64.1.5", "100.64.1.200") || sameNet24("100.64.1.5", "100.64.2.5") || sameNet24("x", "y") {
		t.Fatal("sameNet24")
	}
}
