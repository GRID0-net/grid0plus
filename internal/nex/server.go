package nex

import (
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Handler answers RMC calls on one virtual port.
type Handler interface {
	// ServeRMC answers req. A nil response sends nothing, which is right in
	// two cases: a protocol that never answers (MessageDelivery), and a
	// handler that will answer later from another goroutine with Conn.Reply.
	ServeRMC(c *Conn, req *Request) *Response
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(c *Conn, req *Request) *Response

func (f HandlerFunc) ServeRMC(c *Conn, req *Request) *Response { return f(c, req) }

// AcceptFunc decides whether a CONNECT is let in. It is given the CONNECT's
// payload and returns what goes back in the CONNECT/ACK. See TicketAcceptor
// for the secure server's; the authentication server has none, and takes
// only an empty payload.
type AcceptFunc func(payload []byte) (Identity, []byte, error)

// Identity is who a connection belongs to, as established at CONNECT.
type Identity struct {
	PID        uint64
	SessionKey []byte
}

// Port is one PRUDP virtual port and what serves it.
//
// On the Switch, one WebSocket listener carries both halves of a game
// server: the authentication server on port 1 and the secure server on port
// 2 (https://github.com/kinnay/NintendoClients/wiki/PRUDP-Protocol#virtual-ports).
// So what a connection is -- anonymous and able to ask only for a ticket, or
// authenticated and able to play -- is decided by the port its packets name,
// not by which socket they came in on.
type Port struct {
	// MinorVersion and SupportedFunctions are the most this port offers. The
	// SYN/ACK answers the lower of this and what the client asked for.
	MinorVersion       uint8
	SupportedFunctions uint32

	// Accept, when set, admits a CONNECT; nil admits only an empty one.
	Accept AcceptFunc

	Handler Handler

	// OnConnect runs once a connection is established, before any of its
	// calls. Returning an error drops it.
	OnConnect func(*Conn) error
	// OnClose runs once when a connection ends, however it ends.
	OnClose func(*Conn)
}

// Server serves NEX over WebSocket.
type Server struct {
	// AccessKey is the title's NEX access key. It is public -- it ships in
	// every copy of the game -- and only gates the CONNECT signature.
	AccessKey string
	Ports     map[uint8]*Port
	Log       *slog.Logger

	// IdleTimeout drops a socket that has sent nothing for this long. The
	// console pings well inside it; a socket that goes quiet is a console
	// that went away without closing, and its matchmaking state must go too.
	IdleTimeout time.Duration

	// TrustForwardedFor lists the peers whose X-Real-IP header is believed.
	// nginx terminates TLS in front of this server, so without it every
	// console would appear to be 127.0.0.1 -- and every station derived from
	// a console's address would point at the server.
	TrustForwardedFor func(peer net.IP) bool

	nextConnID atomic.Uint32

	mu    sync.RWMutex
	conns map[uint32]*Conn
}

// DefaultIdleTimeout is used when IdleTimeout is zero.
const DefaultIdleTimeout = 60 * time.Second

// Loopback trusts X-Real-IP only from the local machine, which is where
// SwitchNet's nginx runs.
func Loopback(peer net.IP) bool { return peer != nil && peer.IsLoopback() }

// ServeHTTP upgrades the request and serves the socket until it closes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !isWebSocketUpgrade(r) {
		http.Error(w, "this is a NEX WebSocket endpoint", http.StatusBadRequest)
		return
	}
	ws, err := upgrade(w, r)
	if err != nil {
		s.log().Warn("nex: websocket upgrade failed", "remote", r.RemoteAddr, "error", err)
		return
	}
	sess := &session{server: s, ws: ws, remoteIP: s.clientIP(r),
		pending: map[uint8][]byte{}, conns: map[connKey]*Conn{}}
	sess.run()
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if fwd := r.Header.Get("X-Real-IP"); fwd != "" && s.TrustForwardedFor != nil &&
		s.TrustForwardedFor(net.ParseIP(host)) && net.ParseIP(fwd) != nil {
		return fwd
	}
	return host
}

func (s *Server) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

// Conn returns a live connection by id.
func (s *Server) Conn(id uint32) *Conn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.conns[id]
}

// Conns returns every live connection.
func (s *Server) Conns() []*Conn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Conn, 0, len(s.conns))
	for _, c := range s.conns {
		out = append(out, c)
	}
	return out
}

func (s *Server) register(c *Conn) {
	s.mu.Lock()
	if s.conns == nil {
		s.conns = map[uint32]*Conn{}
	}
	s.conns[c.ID] = c
	s.mu.Unlock()
}

func (s *Server) unregister(c *Conn) {
	s.mu.Lock()
	if s.conns[c.ID] == c {
		delete(s.conns, c.ID)
	}
	s.mu.Unlock()
}

func (s *Server) newConnID() uint32 {
	for {
		// Never 0: RVCID 0 in a station URL reads as "no connection".
		if id := s.nextConnID.Add(1); id != 0 {
			return id
		}
	}
}

// connKey names a PRUDP connection within one socket: the client's port and
// the server port it is talking to.
type connKey struct{ clientPort, serverPort uint8 }

// session is one WebSocket.
type session struct {
	server   *Server
	ws       *wsConn
	remoteIP string

	// pending holds the connection signature each client port was given in
	// its SYN/ACK, until its CONNECT proves it saw it.
	pending map[uint8][]byte
	conns   map[connKey]*Conn
	partial []byte
}

func (s *session) run() {
	idle := s.server.IdleTimeout
	if idle == 0 {
		idle = DefaultIdleTimeout
	}
	defer func() {
		for _, c := range s.conns {
			c.terminate("socket closed")
		}
		_ = s.ws.Close()
	}()
	for {
		msg, err := s.ws.ReadMessage(idle)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.server.log().Debug("nex: socket ended", "remote", s.remoteIP, "error", err)
			}
			return
		}
		packets, rest, err := DecodePackets(append(s.partial, msg...))
		if err != nil {
			s.server.log().Warn("nex: undecodable packet, dropping socket", "remote", s.remoteIP, "error", err)
			return
		}
		s.partial = append(s.partial[:0], rest...)
		for _, p := range packets {
			if !s.handle(p) {
				return
			}
		}
	}
}

// handle processes one packet. false ends the socket.
func (s *session) handle(p *Packet) bool {
	port := s.server.Ports[p.DestPort]
	if port == nil {
		s.server.log().Warn("nex: packet for a port nothing serves", "remote", s.remoteIP, "port", p.DestPort)
		return false
	}
	key := connKey{clientPort: p.SourcePort, serverPort: p.DestPort}

	switch {
	case p.Type == PacketSYN && !p.Has(FlagACK):
		return s.syn(p, port)
	case p.Type == PacketCONNECT && !p.Has(FlagACK):
		return s.connect(p, port, key)
	}

	c := s.conns[key]
	if c == nil {
		// A late packet for a connection that already ended. Harmless.
		return true
	}
	c.handle(p)
	if c.isClosed() {
		// A DISCONNECT. The socket has nothing left on it once its last
		// connection is gone; the console opens a new one to reconnect.
		delete(s.conns, key)
		return len(s.conns) > 0
	}
	return true
}

func (s *session) syn(p *Packet, port *Port) bool {
	sig := make([]byte, 16)
	_, _ = rand.Read(sig)
	s.pending[p.SourcePort] = sig

	ack := &Packet{
		SourceType: p.DestType, DestType: p.SourceType,
		SourcePort: p.DestPort, DestPort: p.SourcePort,
		Type: PacketSYN, Flags: FlagACK,
		MinorVersion:        min(port.MinorVersion, p.MinorVersion),
		SupportedFunctions:  port.SupportedFunctions & p.SupportedFunctions,
		ConnectionSignature: sig,
	}
	return s.send(ack) == nil
}

func (s *session) connect(p *Packet, port *Port, key connKey) bool {
	if c := s.conns[key]; c != nil {
		// Our CONNECT/ACK was lost or slow and the client resent. Answer
		// again, identically; the connection already exists.
		return s.send(c.connectAck) == nil
	}

	sig, ok := s.pending[p.SourcePort]
	if !ok {
		s.server.log().Warn("nex: CONNECT without a SYN", "remote", s.remoteIP)
		return false
	}
	if !equalBytes(p.LiteSignature, liteSignature(s.server.AccessKey, sig)) {
		// Either a client for another title (a different access key) or not a
		// NEX client at all.
		s.server.log().Warn("nex: CONNECT signature does not match this title's access key", "remote", s.remoteIP)
		return false
	}
	if p.MinorVersion > port.MinorVersion {
		s.server.log().Warn("nex: CONNECT asks for a newer PRUDP version than was offered",
			"remote", s.remoteIP, "asked", p.MinorVersion, "offered", port.MinorVersion)
		return false
	}
	delete(s.pending, p.SourcePort)

	var id Identity
	var response []byte
	if port.Accept != nil {
		var err error
		id, response, err = port.Accept(p.Payload)
		if err != nil {
			// Refused: no CONNECT/ACK at all, which the console reports as a
			// connection failure. There is no error code to send at this layer.
			s.server.log().Warn("nex: CONNECT refused", "remote", s.remoteIP, "port", p.DestPort, "error", err)
			return false
		}
	} else if len(p.Payload) != 0 {
		s.server.log().Warn("nex: unexpected CONNECT payload on an open port", "remote", s.remoteIP)
		return false
	}

	c := newConn(s, port, key, p, id)
	c.connectAck = &Packet{
		SourceType: p.DestType, DestType: p.SourceType,
		SourcePort: p.DestPort, DestPort: p.SourcePort,
		Type: PacketCONNECT, Flags: FlagACK | FlagHasSize,
		Sequence:           p.Sequence,
		MinorVersion:       p.MinorVersion,
		SupportedFunctions: p.SupportedFunctions,
		Payload:            response,
	}
	s.conns[key] = c
	s.server.register(c)
	if port.OnConnect != nil {
		if err := port.OnConnect(c); err != nil {
			s.server.log().Warn("nex: connection refused after CONNECT", "remote", s.remoteIP, "pid", c.PID, "error", err)
			c.terminate("refused")
			delete(s.conns, key)
			return false
		}
	}
	c.start()
	return s.send(c.connectAck) == nil
}

func (s *session) send(p *Packet) error { return s.ws.WriteMessage(p.Encode()) }

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var d byte
	for i := range a {
		d |= a[i] ^ b[i]
	}
	return d == 0
}
