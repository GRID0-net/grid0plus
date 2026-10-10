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

func TestAFrontlineFrameReadsBackAsItsTypeAndBody(t *testing.T) {
	frame := frontlineFrame(frontlinePong, time.Unix(1700000000, 0), func(w *fbWriter) int {
		pos, _ := w.table([]*fbField{u64(0xfeedface)})
		return pos
	})
	if n := binary.LittleEndian.Uint32(frame); int(n) != len(frame)-4 {
		t.Fatalf("size prefix %d for %d payload bytes", n, len(frame)-4)
	}
	p := frame[4:]
	if got := frontlineFrameType(p); got != frontlinePong {
		t.Fatalf("type = %d", got)
	}
	body, ok := frontlineBody(p)
	if !ok {
		t.Fatal("no body")
	}
	if id, ok := fbU64(p, body, 0); !ok || id != 0xfeedface {
		t.Fatalf("pong id = %#x, %v", id, ok)
	}
	root, _ := fbTable(p, 0)
	if ts, ok := fbU64(p, root, 3); !ok || ts != 1700000000 {
		t.Fatalf("time = %d, %v", ts, ok)
	}
	if pos, ok := fbFieldPos(p, root, 3); !ok || pos%8 != 0 {
		t.Fatalf("u64 field at %d is not 8-aligned", pos)
	}
}

// npns only reaches state 8 once its twin sync ends, and aborts on a Pong it
// did not ask for: the frontline must answer RootHash and Ping, and only them.
func TestTheFrontlineAnswersARootHashWithAnEmptyOneAndPongsAPing(t *testing.T) {
	s := newServer(t, t.TempDir())
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\n\r\n", frontlineHost)
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	readFrame := func() []byte {
		t.Helper()
		f, err := readFrontlineFrame(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	sendChunk := func(frame []byte) {
		t.Helper()
		if _, err := fmt.Fprintf(conn, "%x\r\n%s\r\n", len(frame), frame); err != nil {
			t.Fatal(err)
		}
	}
	if got := frontlineFrameType(readFrame()); got != 12 {
		t.Fatalf("opening frame type = %d, want HandoverResult", got)
	}

	rootHash := frontlineFrame(frontlineRootHash, time.Unix(1, 0), func(w *fbWriter) int {
		pos, _ := w.table([]*fbField{u64(42)})
		return pos
	})
	sendChunk(rootHash)
	// The server's RootHash, listing no collections: an echo of the console's
	// own list stalls npns asking for leaves the server does not have.
	reply := readFrame()
	if frontlineFrameType(reply) != frontlineRootHash {
		t.Fatalf("reply type = %d, want RootHash", frontlineFrameType(reply))
	}
	body, ok := frontlineBody(reply)
	if !ok {
		t.Fatal("RootHash reply has no body")
	}
	slot, ok := fbFieldPos(reply, body, 0)
	if !ok {
		t.Fatal("RootHash reply omits its collection list, which npns reads unchecked")
	}
	vec, _ := fbTable(reply, slot)
	if n := binary.LittleEndian.Uint32(reply[vec:]); n != 0 {
		t.Fatalf("RootHash reply lists %d collections, want none", n)
	}

	ping := frontlineFrame(frontlinePing, time.Unix(2, 0), func(w *fbWriter) int {
		pos, _ := w.table([]*fbField{u64(0x1234), u64(5), u64(6)})
		return pos
	})
	sendChunk(ping)
	pong := readFrame()
	if frontlineFrameType(pong) != frontlinePong {
		t.Fatalf("reply type = %d, want Pong", frontlineFrameType(pong))
	}
	body, _ = frontlineBody(pong)
	if id, _ := fbU64(pong, body, 0); id != 0x1234 {
		t.Fatalf("pong id = %#x", id)
	}

	// Anything else is only logged.
	sendChunk(frontlineFrame(9, time.Unix(3, 0), func(w *fbWriter) int { pos, _ := w.table(nil); return pos }))
	conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var b [1]byte
	if n, _ := io.ReadFull(resp.Body, b[:]); n != 0 {
		t.Fatal("the frontline answered a frame it should only log")
	}
}
