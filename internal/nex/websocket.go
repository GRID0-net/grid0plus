package nex

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A minimal RFC 6455 server, just enough for NEX: binary messages, ping/pong
// and close. Written here rather than imported for one reason that matters:
// the console's upgrade request is not one a strict library accepts
// (gorilla/websocket refuses it on its Sec-WebSocket-Key). So this accepts any
// key it is given -- the key exists to stop a cache replaying an old response,
// which is not a threat that applies to a console on the operator's own network
// -- and otherwise follows the RFC.

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// maxMessageSize bounds one reassembled WebSocket message. The largest thing
// NEX sends in one is a PRUDP fragment (about 1.3 KB); a megabyte is far past
// anything legitimate and far below anything that hurts.
const maxMessageSize = 1 << 20

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// wsConn is one upgraded connection.
type wsConn struct {
	conn net.Conn
	r    *bufio.Reader

	writeMu sync.Mutex
	closed  bool
}

// isWebSocketUpgrade reports whether r asks for a WebSocket. Either header is
// enough: an HTTP request with neither is not one, whatever its path.
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		r.Header.Get("Sec-WebSocket-Key") != ""
}

// upgrade completes the handshake. The "NEX" subprotocol is echoed when the
// client offers it: the console offers it, and a client that offers a
// subprotocol is entitled to refuse a response that does not pick one.
func upgrade(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("websocket: response cannot be hijacked")
	}

	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + websocketGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])

	protocol := ""
	for _, offered := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(offered, ",") {
			if strings.EqualFold(strings.TrimSpace(p), "NEX") {
				protocol = strings.TrimSpace(p)
			}
		}
	}

	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("websocket: hijack: %w", err)
	}
	var b strings.Builder
	b.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	b.WriteString("Upgrade: websocket\r\n")
	b.WriteString("Connection: Upgrade\r\n")
	b.WriteString("Sec-WebSocket-Accept: " + accept + "\r\n")
	if protocol != "" {
		b.WriteString("Sec-WebSocket-Protocol: " + protocol + "\r\n")
	}
	b.WriteString("\r\n")
	_ = conn.SetDeadline(time.Time{})
	if _, err := conn.Write([]byte(b.String())); err != nil {
		conn.Close()
		return nil, fmt.Errorf("websocket: write handshake: %w", err)
	}
	return &wsConn{conn: conn, r: rw.Reader}, nil
}

// ReadMessage returns the next data message's payload, answering pings and
// reassembling fragmented messages on the way. io.EOF means a clean close.
func (c *wsConn) ReadMessage(idle time.Duration) ([]byte, error) {
	var msg []byte
	var inMessage bool
	for {
		if idle > 0 {
			_ = c.conn.SetReadDeadline(time.Now().Add(idle))
		}
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			_ = c.writeFrame(opClose, payload)
			return nil, io.EOF
		case opText, opBinary:
			if inMessage {
				return nil, errors.New("websocket: new message inside a fragmented one")
			}
			inMessage = true
			msg = append(msg[:0], payload...)
		case opContinuation:
			if !inMessage {
				return nil, errors.New("websocket: continuation with no message")
			}
			msg = append(msg, payload...)
		default:
			return nil, fmt.Errorf("websocket: unknown opcode %#x", op)
		}
		if len(msg) > maxMessageSize {
			return nil, errors.New("websocket: message too large")
		}
		if fin {
			return msg, nil
		}
	}
}

func (c *wsConn) readFrame() (fin bool, op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.r, h[:]); err != nil {
		return
	}
	fin = h[0]&0x80 != 0
	op = h[0] & 0x0F
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.r, ext[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.r, ext[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	if n > maxMessageSize {
		err = errors.New("websocket: frame too large")
		return
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.r, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.r, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
	}
	return
}

// WriteMessage sends one binary message. Safe for concurrent use: replies,
// acknowledgements and server-pushed notifications all write to the same
// socket from different goroutines.
func (c *wsConn) WriteMessage(payload []byte) error {
	return c.writeFrame(opBinary, payload)
}

func (c *wsConn) writeFrame(op byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	h := make([]byte, 0, 10+len(payload))
	h = append(h, 0x80|op)
	switch n := len(payload); {
	case n < 126:
		h = append(h, byte(n))
	case n <= 0xFFFF:
		h = append(h, 126)
		h = binary.BigEndian.AppendUint16(h, uint16(n))
	default:
		h = append(h, 127)
		h = binary.BigEndian.AppendUint64(h, uint64(n))
	}
	h = append(h, payload...)
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.conn.Write(h)
	return err
}

// Close sends a close frame and closes the socket.
func (c *wsConn) Close() error {
	_ = c.writeFrame(opClose, nil)
	c.writeMu.Lock()
	c.closed = true
	c.writeMu.Unlock()
	return c.conn.Close()
}
