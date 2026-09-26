package nex_test

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n-popescu/switchnet/internal/nex"
	"github.com/n-popescu/switchnet/internal/nex/nextest"
)

const accessKey = "0123abcd"

type harness struct {
	srv    *nex.Server
	url    string
	mu     sync.Mutex
	closed []uint32
	conns  chan *nex.Conn
}

func newHarness(t *testing.T, handler nex.HandlerFunc, configure func(*nex.Server)) *harness {
	t.Helper()
	h := &harness{conns: make(chan *nex.Conn, 8)}
	h.srv = &nex.Server{
		AccessKey: accessKey,
		Ports: map[uint8]*nex.Port{
			1: {
				MinorVersion: 3,
				Handler:      handler,
				OnConnect:    func(c *nex.Conn) error { h.conns <- c; return nil },
				OnClose: func(c *nex.Conn) {
					h.mu.Lock()
					h.closed = append(h.closed, c.ID)
					h.mu.Unlock()
				},
			},
		},
		TrustForwardedFor: nex.Loopback,
	}
	if configure != nil {
		configure(h.srv)
	}
	ts := httptest.NewServer(h.srv)
	t.Cleanup(ts.Close)
	h.url = "ws" + strings.TrimPrefix(ts.URL, "http")
	return h
}

func echo(c *nex.Conn, req *nex.Request) *nex.Response { return req.Success(req.Body) }

func TestACallIsAnsweredOverTheSocket(t *testing.T) {
	h := newHarness(t, echo, nil)
	c, err := nextest.Dial(h.url, nextest.Options{AccessKey: accessKey, Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Call(0x6E, 7, []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK() || resp.Protocol != 0x6E || resp.Method != 7 || !bytes.Equal(resp.Body, []byte{1, 2, 3}) {
		t.Fatalf("response %+v", resp)
	}
}

func TestAMessageLargerThanOneFragmentSurvivesBothWays(t *testing.T) {
	// A login carrying an id token is several kilobytes: it spans fragments
	// on the way in, and nothing may be lost or reordered on the way out.
	h := newHarness(t, echo, nil)
	c, err := nextest.Dial(h.url, nextest.Options{AccessKey: accessKey, Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	big := bytes.Repeat([]byte("0123456789"), 500)
	resp, err := c.Call(0x0A, 2, big)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(resp.Body, big) {
		t.Fatalf("got %d bytes back, sent %d", len(resp.Body), len(big))
	}
}

func TestTheSynAckOffersTheLowerMinorVersion(t *testing.T) {
	h := newHarness(t, echo, nil)
	c, err := nextest.Dial(h.url, nextest.Options{AccessKey: accessKey, Port: 1, MinorVersion: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.MinorVersion != 3 {
		t.Fatalf("negotiated %d, want the server's 3", c.MinorVersion)
	}
}

func TestAConnectSignedWithAnotherTitlesKeyIsRefused(t *testing.T) {
	h := newHarness(t, echo, nil)
	if _, err := nextest.Dial(h.url, nextest.Options{AccessKey: "ffffffff", Port: 1}); err == nil {
		t.Fatal("a CONNECT signed with the wrong access key was accepted")
	}
}

func TestAConnectPayloadOnAPortWithNoAcceptIsRefused(t *testing.T) {
	h := newHarness(t, echo, nil)
	if _, err := nextest.Dial(h.url, nextest.Options{AccessKey: accessKey, Port: 1,
		ConnectPayload: []byte{1}}); err == nil {
		t.Fatal("an unauthenticated port accepted a CONNECT payload")
	}
}

func TestAPortNothingServesIsRefused(t *testing.T) {
	h := newHarness(t, echo, nil)
	if _, err := nextest.Dial(h.url, nextest.Options{AccessKey: accessKey, Port: 9}); err == nil {
		t.Fatal("port 9 answered")
	}
}

func TestAcceptSetsTheConnectionsIdentityAndResponse(t *testing.T) {
	h := newHarness(t, echo, func(s *nex.Server) {
		s.Ports[2] = &nex.Port{
			Handler: nex.HandlerFunc(func(c *nex.Conn, req *nex.Request) *nex.Response {
				w := nex.NewWriter()
				w.PID(c.PID)
				return req.Success(w.Bytes())
			}),
			Accept: func(payload []byte) (nex.Identity, []byte, error) {
				return nex.Identity{PID: 1234}, []byte{0xAA}, nil
			},
		}
	})
	c, err := nextest.Dial(h.url, nextest.Options{AccessKey: accessKey, Port: 2, ConnectPayload: []byte("ticket")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !bytes.Equal(c.ConnectResponse, []byte{0xAA}) {
		t.Fatalf("CONNECT/ACK payload %x", c.ConnectResponse)
	}
	resp, err := c.Call(1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(resp.Body); got != "d204000000000000" {
		t.Fatalf("pid on the wire %s", got)
	}
}

func TestADisconnectEndsTheConnection(t *testing.T) {
	h := newHarness(t, echo, nil)
	c, err := nextest.Dial(h.url, nextest.Options{AccessKey: accessKey, Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	conn := <-h.conns
	_ = c.Close()
	select {
	case <-conn.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("OnClose never ran")
	}
	if h.srv.Conn(conn.ID) != nil {
		t.Fatal("a closed connection is still registered")
	}
}

func TestAServerCallReachesTheConsole(t *testing.T) {
	h := newHarness(t, echo, nil)
	c, err := nextest.Dial(h.url, nextest.Options{AccessKey: accessKey, Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn := <-h.conns
	if err := conn.Call(0x0E, 1, []byte{9}); err != nil {
		t.Fatal(err)
	}
	n, err := c.Notification(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if n.Protocol != 0x0E || n.Method != 1 || !bytes.Equal(n.Body, []byte{9}) {
		t.Fatalf("notification %+v", n)
	}
}

func TestAPanickingHandlerAnswersAnErrorAndKeepsTheConnection(t *testing.T) {
	h := newHarness(t, func(c *nex.Conn, req *nex.Request) *nex.Response {
		if req.Method == 1 {
			panic("boom")
		}
		return req.Success(nil)
	}, nil)
	c, err := nextest.Dial(h.url, nextest.Options{AccessKey: accessKey, Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	resp, err := c.Call(1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK() {
		t.Fatal("a panic was answered as success")
	}
	if resp, err := c.Call(1, 2, nil); err != nil || !resp.OK() {
		t.Fatalf("connection unusable after a panic: %v %+v", err, resp)
	}
}

func TestReliablePingsDoNotStallTheCallsBehindThem(t *testing.T) {
	// A reliable PING consumes a sequence id. If the window did not account
	// for it, every call after the first ping would wait forever for a DATA
	// with that id.
	h := newHarness(t, echo, nil)
	c, err := nextest.Dial(h.url, nextest.Options{AccessKey: accessKey, Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Ping(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(1, 1, nil); err != nil {
		t.Fatal(err)
	}
}

func TestXRealIPIsBelievedOnlyFromATrustedPeer(t *testing.T) {
	hdr := http.Header{"X-Real-Ip": []string{"203.0.113.9"}}

	trusted := newHarness(t, echo, nil)
	c, err := nextest.Dial(trusted.url, nextest.Options{AccessKey: accessKey, Port: 1, Header: hdr})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if ip := (<-trusted.conns).RemoteIP; ip != "203.0.113.9" {
		t.Fatalf("behind the local edge, RemoteIP = %s", ip)
	}

	untrusted := newHarness(t, echo, func(s *nex.Server) { s.TrustForwardedFor = nil })
	c2, err := nextest.Dial(untrusted.url, nextest.Options{AccessKey: accessKey, Port: 1, Header: hdr})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if ip := (<-untrusted.conns).RemoteIP; ip == "203.0.113.9" {
		t.Fatal("a client-supplied X-Real-IP was believed with no trusted proxy")
	}
}

func TestAnIdleSocketIsDropped(t *testing.T) {
	h := newHarness(t, echo, func(s *nex.Server) { s.IdleTimeout = 100 * time.Millisecond })
	c, err := nextest.Dial(h.url, nextest.Options{AccessKey: accessKey, Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn := <-h.conns
	select {
	case <-conn.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("an idle socket was kept")
	}
}

func TestAPlainHTTPRequestIsNotUpgraded(t *testing.T) {
	h := newHarness(t, echo, nil)
	resp, err := http.Get("http" + strings.TrimPrefix(h.url, "ws"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
