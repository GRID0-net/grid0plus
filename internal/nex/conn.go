package nex

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// FragmentSize is the most payload one DATA packet carries. The value NEX
// itself uses on Switch; see the PRUDP page's "Fragment size".
const FragmentSize = 1300

// maxOutOfOrder bounds how many reliable packets are held waiting for a gap
// to fill. Over a TCP-backed WebSocket packets cannot actually arrive out of
// order, so a real gap is a broken client, not a slow network.
const maxOutOfOrder = 256

// maxAssembled bounds one reassembled RMC message. The largest thing a
// console sends is a login carrying its id token -- a few kilobytes.
const maxAssembled = 1 << 20

// Conn is one PRUDP connection: a console talking to one virtual port.
type Conn struct {
	// ID is this connection's id, unique for the server's lifetime. The
	// secure server hands it back from Register as the connection's RVCID.
	ID uint32
	// PID is the principal the connection authenticated as, taken from its
	// Kerberos ticket at CONNECT. Zero on the authentication server, which
	// does not know who is calling until they log in -- and even then does
	// not attach the result to the connection, because that connection can do
	// nothing but ask for a ticket.
	PID uint64
	// SessionKey is the key from the ticket. Lite does not encrypt with it,
	// but Pia derives its own session keys from gathering data, not from
	// this, so it is kept only for completeness.
	SessionKey []byte
	// RemoteIP is the console's address as the edge saw it.
	RemoteIP string
	// ConnectedAt is when the CONNECT was accepted.
	ConnectedAt time.Time

	Port *Port

	sess       *session
	key        connKey
	streamType uint8
	clientType uint8
	connectAck *Packet

	// Receive side: owned by the socket's read goroutine.
	nextIn  uint16
	ooo     map[uint16]*Packet
	partial []byte

	// Send side.
	sendMu  sync.Mutex
	nextOut uint16
	callID  atomic.Uint32

	calls     chan *Request
	closeOnce sync.Once
	closed    atomic.Bool
	done      chan struct{}

	lastSeen atomic.Int64 // unix nanos

	stateMu sync.Mutex
	state   map[any]any
}

func newConn(s *session, port *Port, key connKey, connect *Packet, id Identity) *Conn {
	c := &Conn{
		ID:          s.server.newConnID(),
		PID:         id.PID,
		SessionKey:  id.SessionKey,
		RemoteIP:    s.remoteIP,
		ConnectedAt: time.Now(),
		Port:        port,
		sess:        s,
		key:         key,
		streamType:  connect.DestType,
		clientType:  connect.SourceType,
		// The CONNECT was reliable packet 1; the first DATA is 2.
		nextIn:  connect.Sequence + 1,
		ooo:     map[uint16]*Packet{},
		nextOut: 1,
		calls:   make(chan *Request, 64),
		done:    make(chan struct{}),
		state:   map[any]any{},
	}
	c.touch()
	return c
}

func (c *Conn) log() *slog.Logger { return c.sess.server.log() }

func (c *Conn) touch() { c.lastSeen.Store(time.Now().UnixNano()) }

// LastSeen is when the console last sent anything on this connection.
func (c *Conn) LastSeen() time.Time { return time.Unix(0, c.lastSeen.Load()) }

// Server is the server this connection belongs to.
func (c *Conn) Server() *Server { return c.sess.server }

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.done }

func (c *Conn) isClosed() bool { return c.closed.Load() }

// Value and SetValue attach per-connection state for the layers above
// (registered stations, NAT properties). Keys should be unexported types, as
// with context.Context.
func (c *Conn) Value(key any) any {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state[key]
}

func (c *Conn) SetValue(key, value any) {
	c.stateMu.Lock()
	c.state[key] = value
	c.stateMu.Unlock()
}

// start runs the call worker. Calls are answered one at a time, in the order
// they arrived -- a console that sends two calls expects them to take effect
// in that order -- but on their own goroutine, so that a slow handler never
// stops the read loop from acknowledging packets. A console that sees no
// acknowledgement resends, and eventually gives up on the connection.
func (c *Conn) start() {
	go func() {
		for {
			select {
			case req := <-c.calls:
				c.serve(req)
			case <-c.done:
				return
			}
		}
	}()
}

func (c *Conn) serve(req *Request) {
	defer func() {
		if r := recover(); r != nil {
			// One malformed request must not take the process down with every
			// other player's connection. Answer it as the client's fault.
			c.log().Error("nex: handler panicked", "pid", c.PID, "protocol", req.Protocol,
				"method", req.Method, "panic", r)
			_ = c.Reply(req.Error(ResultInvalidArgument))
		}
	}()
	resp := c.Port.Handler.ServeRMC(c, req)
	if resp != nil {
		_ = c.Reply(resp)
	}
}

// handle processes one packet for this connection. Runs on the read goroutine.
func (c *Conn) handle(p *Packet) {
	c.touch()
	if p.Has(FlagACK) || p.Has(FlagMultiACK) {
		// Acknowledgements of what we sent. Nothing is ever resent over a
		// TCP-backed socket, so there is nothing to cancel.
		return
	}
	if p.Has(FlagNeedACK) {
		c.ack(p)
	}

	switch p.Type {
	case PacketDISCONNECT:
		c.terminate("client disconnected")
		return
	case PacketDATA, PacketPING:
	default:
		return
	}

	if !p.Has(FlagReliable) {
		if p.Type == PacketDATA {
			c.deliver(p.Payload)
		}
		return
	}

	// Reliable: in sequence order. A PING sent reliably takes a sequence id
	// like a DATA does (NintendoClients assigns both from the same counter),
	// so it has to pass through here even though it carries nothing.
	if seqBefore(p.Sequence, c.nextIn) {
		return // a duplicate we already delivered
	}
	c.ooo[p.Sequence] = p
	if len(c.ooo) > maxOutOfOrder {
		c.log().Warn("nex: sequence gap never filled, dropping connection", "pid", c.PID, "waiting_for", c.nextIn)
		c.Close()
		return
	}
	for {
		next, ok := c.ooo[c.nextIn]
		if !ok {
			return
		}
		delete(c.ooo, c.nextIn)
		c.nextIn++
		if next.Type != PacketDATA {
			continue
		}
		c.partial = append(c.partial, next.Payload...)
		if len(c.partial) > maxAssembled {
			c.log().Warn("nex: reassembled message too large", "pid", c.PID)
			c.Close()
			return
		}
		if next.FragmentID == 0 {
			msg := c.partial
			c.partial = nil
			c.deliver(msg)
		}
	}
}

// seqBefore reports whether a precedes b in 16-bit wrapping order.
func seqBefore(a, b uint16) bool { return int16(a-b) < 0 }

func (c *Conn) deliver(msg []byte) {
	req, resp, err := DecodeRMC(msg)
	if err != nil {
		c.log().Warn("nex: undecodable RMC message", "pid", c.PID, "error", err)
		return
	}
	if resp != nil {
		// The console answering a call we made. Only notifications are sent
		// that way, and the console does not answer those, so this is only
		// ever logged.
		c.log().Debug("nex: response to a server call", "pid", c.PID, "protocol", resp.Protocol, "result", resp.Result)
		return
	}
	select {
	case c.calls <- req:
	case <-c.done:
	default:
		c.log().Warn("nex: call queue full, dropping connection", "pid", c.PID)
		c.Close()
	}
}

func (c *Conn) ack(p *Packet) {
	a := &Packet{
		SourceType: p.DestType, DestType: p.SourceType,
		SourcePort: p.DestPort, DestPort: p.SourcePort,
		FragmentID: p.FragmentID,
		Type:       p.Type, Flags: FlagACK,
		Sequence: p.Sequence,
	}
	n := 1
	if p.Type == PacketDISCONNECT {
		// Acknowledged three times, as NEX itself does, so that one lost
		// acknowledgement does not leave the console resending.
		n = 3
	}
	for i := 0; i < n; i++ {
		_ = c.sess.send(a)
	}
}

// Reply sends an RMC response.
func (c *Conn) Reply(resp *Response) error { return c.send(resp.Encode()) }

// Call sends an RMC request from the server -- a notification. Its call id
// comes from a separate range from the console's own so that the two can
// never be confused in a capture.
func (c *Conn) Call(protocol uint16, method uint32, body []byte) error {
	req := &Request{Protocol: protocol, Method: method,
		CallID: 0xFFFF0000 | c.callID.Add(1)&0xFFFF, Body: body}
	return c.send(req.Encode())
}

// send fragments msg into reliable DATA packets.
func (c *Conn) send(msg []byte) error {
	if c.isClosed() {
		return errClosed
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	var frag uint8 = 1
	for {
		chunk := msg
		last := len(chunk) <= FragmentSize
		if !last {
			chunk = msg[:FragmentSize]
		}
		p := &Packet{
			SourceType: c.streamType, DestType: c.clientType,
			SourcePort: c.key.serverPort, DestPort: c.key.clientPort,
			Type:     PacketDATA,
			Flags:    FlagReliable | FlagNeedACK | FlagHasSize,
			Sequence: c.nextOut,
			Payload:  chunk,
		}
		if !last {
			p.FragmentID = frag
			frag++
		}
		c.nextOut++
		if err := c.sess.send(p); err != nil {
			return err
		}
		if last {
			return nil
		}
		msg = msg[FragmentSize:]
	}
}

// Close ends the connection: a DISCONNECT to the console, then the socket.
// The socket goes too because a Switch carries one connection per socket,
// and closing it is what makes the console notice immediately rather than at
// its next timeout.
func (c *Conn) Close() {
	if c.isClosed() {
		return
	}
	_ = c.sess.send(&Packet{
		SourceType: c.streamType, DestType: c.clientType,
		SourcePort: c.key.serverPort, DestPort: c.key.clientPort,
		Type: PacketDISCONNECT,
	})
	c.terminate("closed by server")
	_ = c.sess.ws.Close()
}

// terminate marks the connection ended and runs OnClose, exactly once.
func (c *Conn) terminate(reason string) {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.done)
		c.sess.server.unregister(c)
		c.log().Debug("nex: connection ended", "id", c.ID, "pid", c.PID, "reason", reason)
		if c.Port.OnClose != nil {
			c.Port.OnClose(c)
		}
	})
}

type closedError struct{}

func (closedError) Error() string { return "nex: connection closed" }

var errClosed error = closedError{}
