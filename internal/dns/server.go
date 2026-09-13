package dns

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/n-popescu/switchnet/internal/nintendo"
)

// Config is what the server needs to run.
type Config struct {
	// Listen is the address to serve on. DNS is port 53; a console cannot be
	// told to use another one, so in production this is ":53".
	Listen string

	// RedirectTo is the address Nintendo's hostnames resolve to.
	RedirectTo net.IP
	// NATCheckSecondary is the address nncs2 resolves to. It MUST differ from
	// RedirectTo: Pia probes nncs1 and nncs2 as two servers, and if they share an
	// address the console never sends its second NAT probe and NAT detection
	// never completes. Nil falls back to RedirectTo, with a warning.
	NATCheckSecondary net.IP

	// Upstream resolvers for every name SwitchNet does not own, tried in order.
	Upstream []string

	// TTL of synthesised answers, in seconds. Short, so that switching a console
	// back to Nintendo takes effect quickly rather than after an hour of cached
	// records.
	TTL uint32

	// UpstreamTimeout bounds a forwarded query.
	UpstreamTimeout time.Duration

	// LogForwarded logs the names that were forwarded, not just the redirected
	// ones. Useful once, to find a Nintendo host that should be in the list;
	// far too chatty otherwise, because it logs every name the console resolves.
	LogForwarded bool
}

// Server answers DNS queries.
type Server struct {
	cfg Config
	log *slog.Logger

	udp *net.UDPConn
	tcp *net.TCPListener

	// Counters, for the log line at shutdown. A console that is not being
	// redirected shows up as zero redirects, which is the first thing to check.
	redirected atomic.Uint64
	forwarded  atomic.Uint64
	blackholed atomic.Uint64
	dropped    atomic.Uint64
}

// Listen opens the UDP and TCP sockets.
//
// Both, because a stub resolver retries over TCP when a UDP answer is truncated
// and some clients go straight to TCP. Answering on only one is an intermittent
// failure that looks like a network problem.
func Listen(cfg Config, log *slog.Logger) (*Server, error) {
	if cfg.RedirectTo == nil {
		return nil, errors.New("dns: RedirectTo is required")
	}
	if cfg.RedirectTo.To4() == nil {
		return nil, fmt.Errorf("dns: RedirectTo %s is not an IPv4 address", cfg.RedirectTo)
	}
	if len(cfg.Upstream) == 0 {
		return nil, errors.New("dns: at least one upstream resolver is required")
	}
	if cfg.TTL == 0 {
		cfg.TTL = 60
	}
	if cfg.UpstreamTimeout == 0 {
		cfg.UpstreamTimeout = 3 * time.Second
	}

	if cfg.NATCheckSecondary == nil {
		log.Warn("dns: no NAT-check secondary address; nncs1 and nncs2 will " +
			"resolve to the same address, so the console will not send its second " +
			"NAT probe and NAT detection will not complete. Set " +
			"network.natcheck_secondary_ip.")
	} else if cfg.NATCheckSecondary.Equal(cfg.RedirectTo) {
		return nil, errors.New("dns: NATCheckSecondary is the same as RedirectTo; " +
			"they must be different addresses")
	}

	udpAddr, err := net.ResolveUDPAddr("udp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("dns: resolve %s: %w", cfg.Listen, err)
	}
	udp, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("dns: listen udp %s: %w", cfg.Listen, err)
	}

	// TCP binds to the port the UDP socket actually got, not to cfg.Listen.
	//
	// With an explicit port those are the same, but with port 0 the OS picks
	// independently per protocol and the two would land on different ports. A
	// client falling back to TCP would then get connection refused, which looks
	// like the server being down rather than a binding mistake.
	//
	// Retried a few times on EADDRINUSE: a socket that just released this same
	// port (this process's own previous instance on a systemd restart, or --
	// this is where it actually bites -- a sibling test in this package that
	// picked the identical ephemeral UDP port a moment ago and is still a few
	// milliseconds into closing its TCP side) can leave the port transiently
	// unavailable. A fixed production port makes the restart case real, not
	// hypothetical: systemd's RestartSec is far shorter than a lingering
	// socket can take to clear.
	bound := udp.LocalAddr().(*net.UDPAddr)
	tcpAddr := &net.TCPAddr{IP: bound.IP, Port: bound.Port, Zone: bound.Zone}
	var tcp *net.TCPListener
	for attempt := 0; ; attempt++ {
		tcp, err = net.ListenTCP("tcp", tcpAddr)
		if err == nil || !errors.Is(err, syscall.EADDRINUSE) || attempt >= 4 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		_ = udp.Close()
		return nil, fmt.Errorf("dns: listen tcp %s: %w", bound, err)
	}

	return &Server{cfg: cfg, log: log, udp: udp, tcp: tcp}, nil
}

// Addr returns the address the server is listening on, for both UDP and TCP.
func (s *Server) Addr() string { return s.udp.LocalAddr().String() }

// Serve handles queries until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	var wg sync.WaitGroup

	wg.Add(2)
	go func() { defer wg.Done(); s.serveUDP(ctx) }()
	go func() { defer wg.Done(); s.serveTCP(ctx) }()

	<-ctx.Done()
	_ = s.Close()
	wg.Wait()

	s.log.Info("dns: stopped",
		"redirected", s.redirected.Load(),
		"forwarded", s.forwarded.Load(),
		"blackholed", s.blackholed.Load(),
		"dropped", s.dropped.Load())
	return nil
}

// Close closes both listeners.
func (s *Server) Close() error {
	var errs []error
	if err := s.udp.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		errs = append(errs, err)
	}
	if err := s.tcp.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Server) serveUDP(ctx context.Context) {
	buf := make([]byte, maxUDPMessage)
	for {
		n, from, err := s.udp.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Warn("dns: udp read", "error", err)
			continue
		}

		// Copy: the buffer is reused by the next read.
		query := make([]byte, n)
		copy(query, buf[:n])

		// One goroutine per query, so a slow upstream does not stall the console's
		// next lookup. A console makes a handful of queries at a time; there is no
		// need for a worker pool.
		go func() {
			reply := s.handle(ctx, query, "udp")
			if reply == nil {
				return
			}
			if len(reply) > maxUDPMessage {
				// Our own answers are tiny, so this can only be a forwarded
				// reply. Truncating is what a resolver expects: the client then
				// retries over TCP.
				reply = truncate(reply)
			}
			if _, err := s.udp.WriteToUDP(reply, from); err != nil {
				s.log.Warn("dns: udp write", "to", from, "error", err)
			}
		}()
	}
}

func (s *Server) serveTCP(ctx context.Context) {
	for {
		conn, err := s.tcp.AcceptTCP()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Warn("dns: tcp accept", "error", err)
			continue
		}
		go s.handleTCPConn(ctx, conn)
	}
}

func (s *Server) handleTCPConn(ctx context.Context, conn *net.TCPConn) {
	defer func() { _ = conn.Close() }()

	// A client that connects and says nothing must not hold the connection.
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return
	}

	for {
		query, err := readTCPMessage(conn)
		if err != nil {
			return
		}
		reply := s.handle(ctx, query, "tcp")
		if reply == nil {
			return
		}
		if err := writeTCPMessage(conn, reply); err != nil {
			return
		}
		// Allow another query on the same connection, which is what a resolver
		// doing TCP fallback for several names will do.
		if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return
		}
	}
}

// handle answers one query, or returns nil to drop it.
func (s *Server) handle(ctx context.Context, query []byte, transport string) []byte {
	h, q, err := parseQuery(query)
	if err != nil {
		// Not a query this server can reason about. If we at least have a header,
		// forwarding it is better than dropping: it might be something the
		// upstream understands and we do not.
		if len(query) >= headerLen {
			s.forwarded.Add(1)
			return s.forward(ctx, query)
		}
		s.dropped.Add(1)
		s.log.Debug("dns: dropped", "error", err, "transport", transport)
		return nil
	}

	// A class other than IN is not something SwitchNet has an opinion about.
	if q.Class != classIN && q.Class != TypeANY {
		s.forwarded.Add(1)
		return s.forward(ctx, query)
	}

	resolution, host, matched := nintendo.Resolve(q.Name)
	if !matched {
		s.forwarded.Add(1)
		if s.cfg.LogForwarded {
			s.log.Debug("dns: forwarded", "name", q.Name, "type", typeName(q.Type))
		}
		return s.forward(ctx, query)
	}

	switch resolution {
	case nintendo.ResolveForward:
		// A host that is deliberately not redirected — the system update hosts,
		// and the phase 3 eShop hosts until there is something behind them.
		s.forwarded.Add(1)
		s.log.Debug("dns: passthrough", "name", q.Name, "reason", host.Comment)
		return s.forward(ctx, query)

	case nintendo.ResolveBlackhole:
		s.blackholed.Add(1)
		s.log.Debug("dns: blackholed", "name", q.Name, "type", typeName(q.Type))
		if wantsAddress(q.Type) {
			// 0.0.0.0 rather than NXDOMAIN, so the console fails to connect
			// immediately instead of retrying the lookup.
			return answer(query, h, q, net.IPv4zero, s.cfg.TTL)
		}
		return answer(query, h, q, nil, s.cfg.TTL)

	case nintendo.ResolveNATCheckSecondary:
		ip := s.cfg.NATCheckSecondary
		if ip == nil {
			ip = s.cfg.RedirectTo
		}
		s.redirected.Add(1)
		s.log.Debug("dns: redirected", "name", q.Name, "type", typeName(q.Type), "to", ip)
		if wantsAddress(q.Type) {
			return answer(query, h, q, ip, s.cfg.TTL)
		}
		return answer(query, h, q, nil, s.cfg.TTL)

	default: // nintendo.ResolveRedirect
		s.redirected.Add(1)
		s.log.Debug("dns: redirected",
			"name", q.Name, "type", typeName(q.Type), "to", s.cfg.RedirectTo,
			"service", host.Service)
		if wantsAddress(q.Type) {
			return answer(query, h, q, s.cfg.RedirectTo, s.cfg.TTL)
		}
		// AAAA, HTTPS and anything else get NODATA.
		//
		// This is the branch that matters most. Forwarding an AAAA query for a
		// redirected name would hand the console Nintendo's real IPv6 address,
		// and a console that prefers IPv6 would then talk straight past
		// SwitchNet — intermittently, depending on the network.
		return answer(query, h, q, nil, s.cfg.TTL)
	}
}

// forward relays a query to the first upstream that answers.
func (s *Server) forward(ctx context.Context, query []byte) []byte {
	for _, upstream := range s.cfg.Upstream {
		reply, err := s.forwardTo(ctx, upstream, query)
		if err == nil {
			return reply
		}
		s.log.Debug("dns: upstream failed", "upstream", upstream, "error", err)
	}

	// Every upstream failed. SERVFAIL, so the client retries, rather than
	// NXDOMAIN, which it would cache as "this name does not exist".
	h, q, err := parseQuery(query)
	if err != nil {
		return nil
	}
	s.log.Warn("dns: every upstream failed", "name", q.Name)
	return errorResponse(query, h, q, rcodeservFail)
}

func (s *Server) forwardTo(ctx context.Context, upstream string, query []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.UpstreamTimeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", upstream)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}

	if _, err := conn.Write(query); err != nil {
		return nil, err
	}

	buf := make([]byte, maxUDPMessage)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	if n < headerLen {
		return nil, fmt.Errorf("dns: upstream reply of %d bytes", n)
	}
	// The transaction id must match, or this is a stray or spoofed packet.
	if string(buf[0:2]) != string(query[0:2]) {
		return nil, errors.New("dns: upstream reply has the wrong transaction id")
	}
	return buf[:n], nil
}

// truncate sets the TC bit and drops everything after the question, telling the
// client to retry over TCP.
func truncate(reply []byte) []byte {
	if len(reply) < headerLen {
		return reply
	}
	out := make([]byte, headerLen)
	copy(out, reply[:headerLen])
	flags := binary.BigEndian.Uint16(out[2:4]) | 0x0200 // TC
	binary.BigEndian.PutUint16(out[2:4], flags)
	binary.BigEndian.PutUint16(out[6:8], 0)  // ancount
	binary.BigEndian.PutUint16(out[8:10], 0) // nscount
	binary.BigEndian.PutUint16(out[10:12], 0)
	return out
}

func readTCPMessage(conn net.Conn) ([]byte, error) {
	var lengthBuf [2]byte
	if _, err := readFull(conn, lengthBuf[:]); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint16(lengthBuf[:]))
	if length == 0 || length > maxTCPMessage {
		return nil, fmt.Errorf("dns: tcp message of %d bytes", length)
	}
	msg := make([]byte, length)
	if _, err := readFull(conn, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func writeTCPMessage(conn net.Conn, msg []byte) error {
	buf := make([]byte, 2+len(msg))
	binary.BigEndian.PutUint16(buf[0:2], uint16(len(msg)))
	copy(buf[2:], msg)
	_, err := conn.Write(buf)
	return err
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// typeName is for log lines.
func typeName(t uint16) string {
	switch t {
	case TypeA:
		return "A"
	case TypeAAAA:
		return "AAAA"
	case TypeCNAME:
		return "CNAME"
	case TypeHTTPS:
		return "HTTPS"
	case TypeANY:
		return "ANY"
	default:
		return fmt.Sprintf("TYPE%d", t)
	}
}
