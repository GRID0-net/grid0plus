package nplnrest

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFrontlineSendsItsOpeningFrameAndSurvivesRequestEOF(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "empty POST"
		if streaming {
			name = "streamed POST"
		}
		t.Run(name, func(t *testing.T) {
			s := newServer(t, t.TempDir())
			finished := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(finished)
				s.Handler().ServeHTTP(w, r)
			}))
			defer server.Close()
			conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			framing := "Content-Length: 0"
			if streaming {
				framing = "Transfer-Encoding: chunked"
			}
			if _, err = fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: %s\r\n%s\r\n\r\n", frontlineHost, framing); err != nil {
				t.Fatal(err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
			if err != nil {
				t.Fatalf("response headers before request body ends: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			if resp.Header.Get("Content-Type") != "application/octet-stream" {
				t.Fatalf("content type = %q", resp.Header.Get("Content-Type"))
			}
			frame := make([]byte, 68)
			if _, err := io.ReadFull(resp.Body, frame); err != nil {
				t.Fatalf("opening frame: %v", err)
			}
			checkFrontlineFrame(t, frame, s.now().Unix())
			if streaming {
				if _, err = io.WriteString(conn, "3\r\nabc\r\n0\r\n\r\n"); err != nil {
					t.Fatal(err)
				}
			}
			conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			var next [1]byte
			n, err := resp.Body.Read(next[:])
			if timeout, ok := err.(net.Error); n != 0 || !ok || !timeout.Timeout() {
				t.Fatalf("response must remain idle after request EOF; got %d bytes, %v", n, err)
			}
			conn.Close()
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("Frontline did not stop after client disconnection")
			}
		})
	}
}

// Inspect the wire as a size-prefixed table, not through the production encoder.
func checkFrontlineFrame(t *testing.T, b []byte, wantTime int64) {
	t.Helper()
	u16 := func(i int) int { return int(binary.LittleEndian.Uint16(b[i : i+2])) }
	u32 := func(i int) int { return int(binary.LittleEndian.Uint32(b[i : i+4])) }
	if u32(0) != len(b)-4 {
		t.Fatal("invalid frame length prefix")
	}
	table := 4 + u32(4)
	vt := table - u32(table)
	if u16(vt) != 12 || u16(vt+2) != 28 {
		t.Fatal("invalid opening table layout")
	}
	field := func(n int) int { return table + u16(vt+4+2*n) }
	if b[field(0)] != 12 {
		t.Fatalf("payload discriminator = %d, want captured value 12", b[field(0)])
	}
	strPos := field(2) + u32(field(2))
	if string(b[strPos+4:strPos+4+u32(strPos)]) != "fro" {
		t.Fatal("unexpected sender")
	}
	if got := int64(binary.LittleEndian.Uint64(b[field(3):])); got != wantTime {
		t.Fatalf("timestamp = %d, want %d", got, wantTime)
	}
	payload := field(1) + u32(field(1))
	payloadVT := payload - u32(payload)
	if u16(payloadVT) != 4 || u16(payloadVT+2) != 4 {
		t.Fatal("expected an empty payload table")
	}
}
