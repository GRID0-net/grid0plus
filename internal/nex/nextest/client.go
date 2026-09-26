// Package nextest is a NEX client for tests: just enough of a console to
// open a PRUDP-Lite connection over a WebSocket, make calls and receive the
// server's notifications. It follows the client side of Kinnay's
// NintendoClients (nintendo/nex/prudp.py) rather than this repository's
// server, so that a test passing means two independent readings of the
// protocol agree.
package nextest

import (
	"bufio"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/n-popescu/switchnet/internal/nex"
)

// Client is one PRUDP connection on its own socket.
type Client struct {
	conn net.Conn
	r    *bufio.Reader

	writeMu sync.Mutex
	port    uint8
	seq     uint16
	callID  uint32

	mu       sync.Mutex
	waiting  map[uint32]chan *nex.Response
	notifs   chan *nex.Request
	partial  []byte
	inbuf    []byte
	queue    []*nex.Packet
	closed   chan struct{}
	closeErr error

	// ConnectResponse is the CONNECT/ACK's payload.
	ConnectResponse []byte
	// MinorVersion is what the SYN/ACK negotiated.
	MinorVersion uint8
}

// Options for Dial.
type Options struct {
	AccessKey string
	// Port is the server's virtual port: 1 authentication, 2 secure.
	Port uint8
	// ConnectPayload goes in the CONNECT (a ticket, for the secure port).
	ConnectPayload []byte
	// Header is added to the upgrade request (X-Real-IP, in tests).
	Header http.Header
	// MinorVersion is what the SYN offers. Zero offers 5.
	MinorVersion uint8
}

// Dial connects to a ws:// URL.
func Dial(rawURL string, o Options) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	k := base64.StdEncoding.EncodeToString(key)

	req := fmt.Sprintf("GET / HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: NEX\r\n", u.Host, k)
	for name, vals := range o.Header {
		for _, v := range vals {
			req += name + ": " + v + "\r\n"
		}
	}
	req += "\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, err
	}
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	sum := sha1.Sum([]byte(k + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		conn.Close()
		return nil, fmt.Errorf("nextest: upgrade refused: %s", resp.Status)
	}

	c := &Client{conn: conn, r: r, port: o.Port, waiting: map[uint32]chan *nex.Response{},
		notifs: make(chan *nex.Request, 64), closed: make(chan struct{})}
	if err := c.handshake(o); err != nil {
		conn.Close()
		return nil, err
	}
	go c.readLoop()
	return c, nil
}

const clientPort = 0x1F

func (c *Client) handshake(o Options) error {
	minor := o.MinorVersion
	if minor == 0 {
		minor = 5
	}
	if err := c.writePacket(&nex.Packet{SourceType: 10, DestType: 10, SourcePort: clientPort, DestPort: o.Port,
		Type: nex.PacketSYN, Flags: nex.FlagNeedACK, MinorVersion: minor}); err != nil {
		return err
	}
	synAck, err := c.readPacket()
	if err != nil {
		return err
	}
	if synAck.Type != nex.PacketSYN || !synAck.Has(nex.FlagACK) || len(synAck.ConnectionSignature) != 16 {
		return fmt.Errorf("nextest: bad SYN/ACK %+v", synAck)
	}
	c.MinorVersion = synAck.MinorVersion

	ak := md5.Sum([]byte(o.AccessKey))
	mac := hmac.New(md5.New, ak[:])
	mac.Write(ak[:])
	mac.Write(synAck.ConnectionSignature)

	c.seq = 1
	if err := c.writePacket(&nex.Packet{SourceType: 10, DestType: 10, SourcePort: clientPort, DestPort: o.Port,
		Type: nex.PacketCONNECT, Flags: nex.FlagReliable | nex.FlagNeedACK | nex.FlagHasSize,
		Sequence: 1, MinorVersion: synAck.MinorVersion, SupportedFunctions: synAck.SupportedFunctions,
		LiteSignature: mac.Sum(nil), Payload: o.ConnectPayload}); err != nil {
		return err
	}
	c.seq = 2
	ack, err := c.readPacket()
	if err != nil {
		return fmt.Errorf("nextest: no CONNECT/ACK: %w", err)
	}
	if ack.Type != nex.PacketCONNECT || !ack.Has(nex.FlagACK) || ack.Sequence != 1 {
		return fmt.Errorf("nextest: bad CONNECT/ACK %+v", ack)
	}
	c.ConnectResponse = ack.Payload
	return nil
}

// Call makes one call and waits for its response.
func (c *Client) Call(protocol uint16, method uint32, body []byte) (*nex.Response, error) {
	c.mu.Lock()
	c.callID++
	id := c.callID
	ch := make(chan *nex.Response, 1)
	c.waiting[id] = ch
	c.mu.Unlock()

	req := &nex.Request{Protocol: protocol, Method: method, CallID: id, Body: body}
	if err := c.sendData(req.Encode()); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-c.closed:
		return nil, fmt.Errorf("nextest: connection closed: %v", c.closeErr)
	case <-time.After(5 * time.Second):
		return nil, errors.New("nextest: call timed out")
	}
}

// Send makes a call without waiting for a response (for protocols that
// never answer).
func (c *Client) Send(protocol uint16, method uint32, body []byte) error {
	c.mu.Lock()
	c.callID++
	id := c.callID
	c.mu.Unlock()
	return c.sendData((&nex.Request{Protocol: protocol, Method: method, CallID: id, Body: body}).Encode())
}

// Notification waits for the next server-initiated call.
func (c *Client) Notification(timeout time.Duration) (*nex.Request, error) {
	select {
	case n := <-c.notifs:
		return n, nil
	case <-time.After(timeout):
		return nil, errors.New("nextest: no notification")
	}
}

// Closed is closed when the server ends the connection.
func (c *Client) Closed() <-chan struct{} { return c.closed }

// Close ends the connection with a DISCONNECT.
func (c *Client) Close() error {
	_ = c.writePacket(&nex.Packet{SourceType: 10, DestType: 10, SourcePort: clientPort, DestPort: c.port,
		Type: nex.PacketDISCONNECT, Flags: nex.FlagReliable | nex.FlagNeedACK, Sequence: c.nextSeq()})
	return c.conn.Close()
}

func (c *Client) nextSeq() uint16 {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	s := c.seq
	c.seq++
	return s
}

// Ping sends a reliable PING, as NintendoClients does.
func (c *Client) Ping() error {
	return c.writePacket(&nex.Packet{SourceType: 10, DestType: 10, SourcePort: clientPort, DestPort: c.port,
		Type: nex.PacketPING, Flags: nex.FlagReliable | nex.FlagNeedACK, Sequence: c.nextSeq()})
}

func (c *Client) sendData(msg []byte) error {
	var frag uint8 = 1
	for {
		chunk, last := msg, len(msg) <= nex.FragmentSize
		if !last {
			chunk = msg[:nex.FragmentSize]
		}
		p := &nex.Packet{SourceType: 10, DestType: 10, SourcePort: clientPort, DestPort: c.port,
			Type: nex.PacketDATA, Flags: nex.FlagReliable | nex.FlagNeedACK | nex.FlagHasSize,
			Sequence: c.nextSeq(), Payload: chunk}
		if !last {
			p.FragmentID = frag
			frag++
		}
		if err := c.writePacket(p); err != nil {
			return err
		}
		if last {
			return nil
		}
		msg = msg[nex.FragmentSize:]
	}
}

func (c *Client) readLoop() {
	defer close(c.closed)
	for {
		p, err := c.readPacket()
		if err != nil {
			c.closeErr = err
			return
		}
		if p.Has(nex.FlagACK) {
			continue
		}
		if p.Type == nex.PacketDISCONNECT {
			c.closeErr = errors.New("server disconnected")
			return
		}
		if p.Type != nex.PacketDATA {
			continue
		}
		_ = c.writePacket(&nex.Packet{SourceType: 10, DestType: 10, SourcePort: clientPort, DestPort: c.port,
			Type: nex.PacketDATA, Flags: nex.FlagACK, Sequence: p.Sequence, FragmentID: p.FragmentID})
		c.partial = append(c.partial, p.Payload...)
		if p.FragmentID != 0 {
			continue
		}
		msg := c.partial
		c.partial = nil
		req, resp, err := nex.DecodeRMC(msg)
		if err != nil {
			c.closeErr = err
			return
		}
		if req != nil {
			c.notifs <- req
			continue
		}
		c.mu.Lock()
		ch := c.waiting[resp.CallID]
		delete(c.waiting, resp.CallID)
		c.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
}

func (c *Client) writePacket(p *nex.Packet) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	payload := p.Encode()
	frame := []byte{0x82}
	n := len(payload)
	switch {
	case n < 126:
		frame = append(frame, 0x80|byte(n))
	default:
		frame = append(frame, 0x80|126)
		frame = binary.BigEndian.AppendUint16(frame, uint16(n))
	}
	mask := []byte{1, 2, 3, 4}
	frame = append(frame, mask...)
	for i, b := range payload {
		frame = append(frame, b^mask[i&3])
	}
	_, err := c.conn.Write(frame)
	return err
}

func (c *Client) readPacket() (*nex.Packet, error) {
	for len(c.queue) == 0 {
		_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var h [2]byte
		if _, err := io.ReadFull(c.r, h[:]); err != nil {
			return nil, err
		}
		n := int(h[1] & 0x7F)
		if n == 126 {
			var ext [2]byte
			if _, err := io.ReadFull(c.r, ext[:]); err != nil {
				return nil, err
			}
			n = int(binary.BigEndian.Uint16(ext[:]))
		} else if n == 127 {
			var ext [8]byte
			if _, err := io.ReadFull(c.r, ext[:]); err != nil {
				return nil, err
			}
			n = int(binary.BigEndian.Uint64(ext[:]))
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(c.r, buf); err != nil {
			return nil, err
		}
		if h[0]&0x0F == 0x8 {
			return nil, io.EOF
		}
		pkts, rest, err := nex.DecodePackets(append(c.inbuf, buf...))
		if err != nil {
			return nil, err
		}
		c.inbuf = append([]byte(nil), rest...)
		c.queue = append(c.queue, pkts...)
	}
	p := c.queue[0]
	c.queue = c.queue[1:]
	return p, nil
}
