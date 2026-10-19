package rendezvous

import (
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n-popescu/grid0plus/internal/nex"
)

// The relay: for two consoles that cannot punch through to each other, the
// server stands in as each one's public station and forwards between them.
// Off unless configured; limited to the players the operator allows.
//
// One UDP port per PAIR of players, forwarding whatever arrives to the other
// end without reading it. A single shared port cannot work on Splatoon 2:
// Pia's hole-punch probe is 16 bytes carrying no identifier at all, so a shared
// port cannot tell whose it is, and Pia refuses an answer that does not come from
// the peer. With a port per pair, everything on the port belongs to that pair.
//
// Only a console's PUBLIC station is repointed at the relay. The LAN one is
// left alone: it carries the connection id Pia needs to set the session up
// after the punch, and repointing it too hands the peer two identical
// candidates and loses that id.

// RelayConfig configures the relay.
type RelayConfig struct {
	// AdvertiseIP is the address consoles are told to send to.
	AdvertiseIP string
	// ListenIP is what the relay's sockets bind to; empty is every interface.
	ListenIP string
	// PortBase and PortSpan are the range pair ports are taken from.
	PortBase, PortSpan int
	// Allow lists the user ids that may be relayed. An id prefixed with "!"
	// is always relayed, without waiting to see a punch fail. A pair is
	// relayed when either player is allowed.
	Allow []string
	// AllowAll relays anyone who needs it.
	AllowAll bool
	// IdleTimeout closes a pair's port after this much silence.
	IdleTimeout time.Duration
}

// DefaultRelayIdle is two minutes: a match sends continuously, and a
// loading screen does not last that long.
const DefaultRelayIdle = 2 * time.Minute

// failedPunchesForRelay is how many hole-punch failures, with no success at
// all, mark a player as needing the relay.
const failedPunchesForRelay = 3

// Relay forwards between pairs of consoles.
type Relay struct {
	s   *Server
	cfg RelayConfig
	log *slog.Logger

	allow, forced map[uint64]bool

	mu    sync.Mutex
	pairs map[[2]uint64]*relayPair
	ports map[int]bool

	opened, forwarded, rejected atomic.Uint64

	stop chan struct{}
}

type relayPair struct {
	key  [2]uint64
	port int
	conn *net.UDPConn
	ips  [2]string // the players' public addresses, from their NEX connections

	mu     sync.Mutex
	addrs  [2]*net.UDPAddr // learned from the first packet each side sends
	last   time.Time
	fwd    uint64
	reject uint64
}

// ErrRelayConfig is a relay configuration that cannot work.
var ErrRelayConfig = errors.New("rendezvous: relay configuration")

// NewRelay starts a relay for s and installs it as s's station rewriter.
func NewRelay(s *Server, cfg RelayConfig) (*Relay, error) {
	if net.ParseIP(cfg.AdvertiseIP) == nil || cfg.PortBase <= 0 || cfg.PortSpan <= 0 || cfg.PortBase+cfg.PortSpan > 65536 {
		return nil, fmt.Errorf("%w: advertise %q, ports %d+%d", ErrRelayConfig, cfg.AdvertiseIP, cfg.PortBase, cfg.PortSpan)
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultRelayIdle
	}
	r := &Relay{s: s, cfg: cfg, log: s.log.With("component", "relay"),
		allow: map[uint64]bool{}, forced: map[uint64]bool{},
		pairs: map[[2]uint64]*relayPair{}, ports: map[int]bool{}, stop: make(chan struct{})}
	for _, id := range cfg.Allow {
		force := strings.HasPrefix(id, "!")
		pid, err := PIDForUserID(strings.TrimPrefix(id, "!"))
		if err != nil {
			return nil, fmt.Errorf("%w: allow entry %q is not a user id", ErrRelayConfig, id)
		}
		r.allow[pid] = true
		if force {
			r.forced[pid] = true
		}
	}
	s.SetStationRewriter(r.rewrite)
	go r.reap()
	return r, nil
}

// Close stops the relay and closes every pair.
func (r *Relay) Close() {
	r.s.SetStationRewriter(nil)
	close(r.stop)
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, p := range r.pairs {
		_ = p.conn.Close()
		delete(r.pairs, k)
	}
}

// needs reports whether a player cannot be reached directly: a strict NAT,
// or several punches that all failed.
func (r *Relay) needs(pid uint64) bool {
	c := r.s.Player(pid)
	if c == nil {
		return false
	}
	n := NATOf(c)
	return n.Class() == NATStrict || (n.Failures >= failedPunchesForRelay && n.Failures == n.Punches)
}

// Wants reports whether a pair is relayed. Symmetric, so both consoles of a
// pair see each other through the same port.
func (r *Relay) Wants(a, b uint64) bool {
	if a == b || !(r.cfg.AllowAll || r.allow[a] || r.allow[b]) {
		return false
	}
	return r.forced[a] || r.forced[b] || r.needs(a) || r.needs(b)
}

func pairKey(a, b uint64) [2]uint64 {
	if a > b {
		a, b = b, a
	}
	return [2]uint64{a, b}
}

// rewrite is the StationRewriter: the owner's public station, as the viewer
// should see it.
func (r *Relay) rewrite(owner, viewer *nex.Conn, st *nex.StationURL) *nex.StationURL {
	if isPrivate(st.Get("address")) || sameHousehold(owner, viewer) || !r.Wants(owner.PID, viewer.PID) {
		return st
	}
	p, err := r.pair(owner, viewer)
	if err != nil {
		r.log.Warn("relay: no port for pair", "a", owner.PID, "b", viewer.PID, "error", err)
		return st
	}
	c := st.Copy()
	c.Set("address", r.cfg.AdvertiseIP)
	c.SetInt("port", int64(p.port))
	if c.Has("Pa") {
		c.Set("Pa", r.cfg.AdvertiseIP)
	}
	return c
}

// pair returns (opening if needed) the pair's port. The port is derived from
// the two PIDs, so it is the same whichever path asks first.
func (r *Relay) pair(a, b *nex.Conn) (*relayPair, error) {
	key := pairKey(a.PID, b.PID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.pairs[key]; p != nil {
		return p, nil
	}
	h := fnv.New32a()
	fmt.Fprintf(h, "%d:%d", key[0], key[1])
	start := int(h.Sum32() % uint32(r.cfg.PortSpan))
	for i := 0; i < r.cfg.PortSpan; i++ {
		port := r.cfg.PortBase + (start+i)%r.cfg.PortSpan
		if r.ports[port] {
			continue
		}
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(r.cfg.ListenIP), Port: port})
		if err != nil {
			continue // taken by something else; try the next
		}
		p := &relayPair{key: key, port: port, conn: conn, last: time.Now()}
		// The addresses each side may send from: the one its NEX connection
		// comes from.
		for i, pid := range key {
			if c := r.s.Player(pid); c != nil {
				p.ips[i] = c.RemoteIP
			}
		}
		r.pairs[key] = p
		r.ports[port] = true
		r.opened.Add(1)
		go r.loop(p)
		r.log.Info("relay: pair opened", "a", key[0], "b", key[1], "port", port)
		return p, nil
	}
	return nil, errors.New("no free port in the relay's range")
}

// sameNet24 reports whether two IPv4 addresses share a /24. A console under
// carrier-grade NAT can send its UDP from a different address than its
// WebSocket, in the same pool. The /24 keeps admission narrow -- the port is one
// pair's, and closes after two minutes of silence -- without refusing those
// consoles.
func sameNet24(a, b string) bool {
	x, y := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	return x != nil && y != nil && x[0] == y[0] && x[1] == y[1] && x[2] == y[2]
}

// side is which end of the pair src is, or -1 for neither.
func (p *relayPair) side(src *net.UDPAddr) int {
	ip := src.IP.String()
	for i := range 2 {
		if p.ips[i] == ip {
			return i
		}
	}
	for i := range 2 {
		if sameNet24(p.ips[i], ip) && !sameNet24(p.ips[1-i], ip) {
			return i
		}
	}
	return -1
}

func (r *Relay) loop(p *relayPair) {
	buf := make([]byte, 2048)
	for {
		n, src, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		i := p.side(src)
		p.mu.Lock()
		if i < 0 {
			p.reject++
			p.mu.Unlock()
			r.rejected.Add(1)
			continue
		}
		p.addrs[i] = src
		p.last = time.Now()
		dst := p.addrs[1-i]
		if dst != nil {
			p.fwd++
		}
		p.mu.Unlock()
		if dst != nil {
			_, _ = p.conn.WriteToUDP(buf[:n], dst)
			r.forwarded.Add(1)
		}
	}
}

func (r *Relay) reap() {
	t := time.NewTicker(r.cfg.IdleTimeout / 4)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
		}
		r.mu.Lock()
		for k, p := range r.pairs {
			p.mu.Lock()
			idle := time.Since(p.last) > r.cfg.IdleTimeout
			p.mu.Unlock()
			if idle {
				_ = p.conn.Close()
				delete(r.pairs, k)
				delete(r.ports, p.port)
				r.log.Info("relay: pair closed after silence", "a", k[0], "b", k[1], "port", p.port, "forwarded", p.fwd)
			}
		}
		r.mu.Unlock()
	}
}

// RelayPair describes a live pair, for the dashboard.
type RelayPair struct {
	A, B      uint64
	Port      int
	Forwarded uint64
	Rejected  uint64
	IdleSecs  int
}

// RelayStats is the relay's state, for the dashboard.
type RelayStats struct {
	AdvertiseIP                 string
	PortBase, PortSpan          int
	Allowed                     []uint64
	AllowAll                    bool
	Opened, Forwarded, Rejected uint64
	Pairs                       []RelayPair
}

// Stats snapshots the relay.
func (r *Relay) Stats() RelayStats {
	st := RelayStats{AdvertiseIP: r.cfg.AdvertiseIP, PortBase: r.cfg.PortBase, PortSpan: r.cfg.PortSpan,
		AllowAll: r.cfg.AllowAll, Opened: r.opened.Load(), Forwarded: r.forwarded.Load(), Rejected: r.rejected.Load()}
	for pid := range r.allow {
		st.Allowed = append(st.Allowed, pid)
	}
	slices.Sort(st.Allowed)
	r.mu.Lock()
	for _, p := range r.pairs {
		p.mu.Lock()
		st.Pairs = append(st.Pairs, RelayPair{A: p.key[0], B: p.key[1], Port: p.port, Forwarded: p.fwd,
			Rejected: p.reject, IdleSecs: int(time.Since(p.last).Seconds())})
		p.mu.Unlock()
	}
	r.mu.Unlock()
	slices.SortFunc(st.Pairs, func(a, b RelayPair) int { return a.Port - b.Port })
	return st
}
