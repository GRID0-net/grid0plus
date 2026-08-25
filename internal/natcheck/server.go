// Package natcheck implements Nintendo's NAT-check (NCS) protocol.
//
// Hosts: nncs1-lp1.n.n.srv.nintendo.net, nncs2-lp1.n.n.srv.nintendo.net
// Reference: https://github.com/kinnay/NintendoClients/wiki/NAT-Check-Server
//
// The console uses this to learn its external address and to classify its
// router's NAT before attempting peer-to-peer. Gameplay itself is always
// peer-to-peer through Nintendo's Pia library, so this server does not carry a
// single gameplay packet — but if NAT detection never completes, matchmaking
// finds opponents and the match then fails to start.
//
// # The two-address requirement
//
// Message type 2 must be answered from a DIFFERENT ADDRESS, and the console
// probes nncs1 and nncs2 as two separate servers. If both hostnames resolve to
// the same IP, the console de-duplicates the probe, never sends the second one,
// and NAT detection does not finish. In the deployment this implementation was
// checked against, that failure showed up as Mario Kart 8 error 2618-201 and was
// not obviously about NAT at all.
//
// So: point nncs1 and nncs2 at two different addresses. Running with one address
// works, but the console will classify its NAT as stricter than it really is, and
// some titles refuse to matchmake on that basis.
package natcheck

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
)

// messageSize is the fixed size of an NCS message: four big-endian 32-bit fields.
const messageSize = 16

// Message is an NCS message.
//
// Layout, big-endian:
//
//	0x0  4  message type
//	0x4  4  external port  (filled in by the server)
//	0x8  4  external IP    (filled in by the server)
//	0xC  4  the console's own external IP on the way out; the server's local IP
//	        on the way back
type Message struct {
	Type         uint32
	ExternalPort uint32
	ExternalIP   uint32
	LocalIP      uint32
}

// Message types.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/NAT-Check-Server#message-types
const (
	// TypeReachability is answered from the regular address and port. The console
	// uses it to check the server is reachable, to measure round-trip time, and
	// to learn its own external address.
	TypeReachability uint32 = 1
	// TypeDifferentAddress must be answered from a different address AND port.
	// It is how the console determines NAT filtering behaviour.
	TypeDifferentAddress uint32 = 2
	// TypeDifferentPort must be answered from the regular address but a
	// different port.
	TypeDifferentPort uint32 = 3
	// TypeMapping1 and TypeMapping2 are answered from the regular address and
	// port; the console compares the mappings it sees to classify NAT mapping.
	TypeMapping1 uint32 = 4
	TypeMapping2 uint32 = 5

	// The 10x types behave like 1 and 3 and are used by newer firmware.
	TypeReachabilityAlt  uint32 = 101
	TypeDifferentPortAlt uint32 = 102
	TypeReachabilityAlt2 uint32 = 103
)

// Decode parses a message. It returns an error for anything that is not exactly
// messageSize bytes, rather than padding or truncating: a short read here means
// something other than a console is talking to the port.
func Decode(buf []byte) (Message, error) {
	if len(buf) != messageSize {
		return Message{}, fmt.Errorf("natcheck: expected %d bytes, got %d", messageSize, len(buf))
	}
	return Message{
		Type:         binary.BigEndian.Uint32(buf[0:4]),
		ExternalPort: binary.BigEndian.Uint32(buf[4:8]),
		ExternalIP:   binary.BigEndian.Uint32(buf[8:12]),
		LocalIP:      binary.BigEndian.Uint32(buf[12:16]),
	}, nil
}

// Encode serialises a message.
func (m Message) Encode() []byte {
	buf := make([]byte, messageSize)
	binary.BigEndian.PutUint32(buf[0:4], m.Type)
	binary.BigEndian.PutUint32(buf[4:8], m.ExternalPort)
	binary.BigEndian.PutUint32(buf[8:12], m.ExternalIP)
	binary.BigEndian.PutUint32(buf[12:16], m.LocalIP)
	return buf
}

// Config is what the server needs to run.
type Config struct {
	// PrimaryListen is the main socket. Nintendo uses UDP 10025.
	PrimaryListen string
	// SecondaryListen is the "same address, different port" socket, used for
	// message types 3 and 102. Nintendo uses UDP 10125.
	SecondaryListen string
	// AlternateListen is a socket on a DIFFERENT ADDRESS, used for message type
	// 2. Empty means there is no second address, and type 2 is answered from the
	// secondary port instead — which lets detection complete but reports a
	// stricter NAT than the router really has.
	AlternateListen string
	// AdvertisedIP is the address the server reports as its own local IP in the
	// last field of a reply. Empty means it is derived from the socket.
	AdvertisedIP string
}

// Server is the NAT-check responder.
type Server struct {
	cfg Config
	log *slog.Logger

	primary   *net.UDPConn
	secondary *net.UDPConn
	alternate *net.UDPConn

	// localIP is the value put in the reply's last field.
	localIP uint32
}

// Listen opens the sockets.
func Listen(cfg Config, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, log: log}

	var err error
	if s.primary, err = listenUDP(cfg.PrimaryListen); err != nil {
		return nil, err
	}
	if cfg.SecondaryListen != "" {
		if s.secondary, err = listenUDP(cfg.SecondaryListen); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	if cfg.AlternateListen != "" {
		if s.alternate, err = listenUDP(cfg.AlternateListen); err != nil {
			_ = s.Close()
			return nil, err
		}
	} else {
		log.Warn("natcheck: no alternate address configured; " +
			"message type 2 will be answered from the secondary port, so consoles " +
			"will classify their NAT as stricter than it is. Configure " +
			"natcheck.alternate_listen on a second address to fix this.")
	}

	if cfg.AdvertisedIP != "" {
		ip := net.ParseIP(cfg.AdvertisedIP)
		if ip == nil || ip.To4() == nil {
			_ = s.Close()
			return nil, fmt.Errorf("natcheck: advertised IP %q is not an IPv4 address", cfg.AdvertisedIP)
		}
		s.localIP = ipToUint32(ip)
	}

	return s, nil
}

func listenUDP(addr string) (*net.UDPConn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("natcheck: resolve %s: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("natcheck: listen on %s: %w", addr, err)
	}
	return conn, nil
}

// Addrs returns the addresses the server is listening on, for logging.
func (s *Server) Addrs() []string {
	var out []string
	for _, c := range []*net.UDPConn{s.primary, s.secondary, s.alternate} {
		if c != nil {
			out = append(out, c.LocalAddr().String())
		}
	}
	return out
}

// Serve reads and answers messages until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, conn := range []*net.UDPConn{s.primary, s.secondary, s.alternate} {
		if conn == nil {
			continue
		}
		wg.Add(1)
		go func(c *net.UDPConn) {
			defer wg.Done()
			s.readLoop(ctx, c)
		}(conn)
	}

	<-ctx.Done()
	// Closing the sockets is what unblocks the readers.
	_ = s.Close()
	wg.Wait()
	return nil
}

func (s *Server) readLoop(ctx context.Context, conn *net.UDPConn) {
	// One byte more than a message, so an oversized datagram is detected rather
	// than silently truncated to a valid-looking message.
	buf := make([]byte, messageSize+1)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Warn("natcheck: read", "local", conn.LocalAddr(), "error", err)
			continue
		}

		msg, err := Decode(buf[:n])
		if err != nil {
			s.log.Debug("natcheck: discarding datagram", "from", from, "error", err)
			continue
		}
		s.respond(msg, from)
	}
}

// respond answers one message from the socket the message type requires.
func (s *Server) respond(msg Message, from *net.UDPAddr) {
	reply := Message{
		Type: msg.Type,
		// The console learns its external address from these two fields. They
		// are the source address of the datagram as this server saw it, which is
		// precisely the mapping the router created.
		ExternalPort: uint32(from.Port),
		ExternalIP:   ipToUint32(from.IP),
		LocalIP:      s.localIP,
	}

	conn := s.socketFor(msg.Type)
	if conn == nil {
		s.log.Debug("natcheck: no socket for message type", "type", msg.Type, "from", from)
		return
	}
	if s.localIP == 0 {
		// Not configured, so derive it from the socket answering the reply.
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			reply.LocalIP = ipToUint32(addr.IP)
		}
	}

	if _, err := conn.WriteToUDP(reply.Encode(), from); err != nil {
		s.log.Warn("natcheck: write", "to", from, "type", msg.Type, "error", err)
		return
	}
	s.log.Debug("natcheck: replied",
		"type", msg.Type, "to", from, "from", conn.LocalAddr())
}

// socketFor picks the socket a message type must be answered from.
func (s *Server) socketFor(msgType uint32) *net.UDPConn {
	switch msgType {
	case TypeDifferentAddress:
		if s.alternate != nil {
			return s.alternate
		}
		// No second address. Falling back to the secondary port at least lets
		// detection terminate; see the package comment for what it costs.
		if s.secondary != nil {
			return s.secondary
		}
		return s.primary

	case TypeDifferentPort, TypeDifferentPortAlt:
		if s.secondary != nil {
			return s.secondary
		}
		return s.primary

	case TypeReachability, TypeMapping1, TypeMapping2,
		TypeReachabilityAlt, TypeReachabilityAlt2:
		return s.primary

	default:
		// An unknown type is answered from the primary socket rather than
		// dropped: a firmware that added a type we have not seen gets a reply of
		// the right shape, and the log line above records the type so it can be
		// looked into.
		s.log.Info("natcheck: unknown message type, answering from primary", "type", msgType)
		return s.primary
	}
}

// Close closes every socket.
func (s *Server) Close() error {
	var errs []error
	for _, c := range []*net.UDPConn{s.primary, s.secondary, s.alternate} {
		if c != nil {
			if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// ipToUint32 converts an IPv4 address to the wire representation. A non-IPv4
// address yields 0, which the console reads as "unknown".
func ipToUint32(ip net.IP) uint32 {
	v4 := ip.To4()
	if v4 == nil {
		return 0
	}
	return binary.BigEndian.Uint32(v4)
}
