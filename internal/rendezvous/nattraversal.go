package rendezvous

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/n-popescu/switchnet/internal/nex"
)

// NATTraversal (protocol 3): coordinating the hole-punch between two
// consoles. The punch itself is peer to peer; the server's part is telling
// each side to start. https://github.com/kinnay/NintendoClients/wiki/NAT-Traversal-Protocol
const ProtocolNATTraversal = 0x03

const (
	methodRequestProbeInitiation         = 1
	methodInitiateProbe                  = 2
	methodRequestProbeInitiationExt      = 3
	methodReportNATTraversalResult       = 4
	methodReportNATProperties            = 5
	methodGetRelaySignatureKey           = 6
	methodReportNATTraversalResultDetail = 7
)

// NAT is what a console reported about its router (ReportNATProperties).
// Mapping and filtering use the NAT-check values: 0 unknown, 1 independent,
// 2 dependent.
type NAT struct {
	Mapping   uint32
	Filtering uint32
	RTT       uint32
	// Punches and Failures count the console's own hole-punch verdicts.
	Punches, Failures uint32
}

type natKey struct{}

type natState struct{ v atomic.Pointer[NAT] }

func natOf(c *nex.Conn) *natState {
	if v, ok := c.Value(natKey{}).(*natState); ok {
		return v
	}
	st := &natState{}
	st.v.Store(&NAT{})
	c.SetValue(natKey{}, st)
	return st
}

// NATOf returns what a connection has reported about its NAT.
func NATOf(c *nex.Conn) NAT { return *natOf(c).v.Load() }

func updateNAT(c *nex.Conn, f func(*NAT)) {
	st := natOf(c)
	for {
		old := st.v.Load()
		n := *old
		f(&n)
		if st.v.CompareAndSwap(old, &n) {
			return
		}
	}
}

// NATClass buckets a NAT the way players talk about it.
type NATClass int

const (
	NATUnknown  NATClass = iota // not reported
	NATOpen                     // endpoint-independent mapping and filtering
	NATModerate                 // independent mapping, dependent filtering
	NATStrict                   // dependent (symmetric) mapping
)

func (c NATClass) String() string {
	return [...]string{"unknown", "open", "moderate", "strict"}[c]
}

// Class buckets a reported NAT.
func (n NAT) Class() NATClass {
	switch {
	case n.Mapping == 0 && n.Filtering == 0:
		return NATUnknown
	case n.Mapping <= 1 && n.Filtering <= 1:
		return NATOpen
	case n.Mapping <= 1:
		return NATModerate
	}
	return NATStrict
}

// NATCompatible reports whether two consoles can plausibly punch through to
// each other. Only the pairs that reliably fail are refused -- strict with
// strict, strict with moderate -- and an unreported NAT is given the benefit
// of the doubt, because refusing a console before it has reported anything
// would isolate everyone at the start of every session. This is the rule
// NextendoNetwork found necessary (facts only): one strict player dropped into
// a lobby otherwise gives EVERYONE in it 2618-0510.
func NATCompatible(a, b NATClass) bool {
	if a == NATUnknown || b == NATUnknown {
		return true
	}
	bad := func(x, y NATClass) bool { return x == NATStrict && (y == NATStrict || y == NATModerate) }
	return !bad(a, b) && !bad(b, a)
}

func (s *Server) registerNATTraversal() {
	const p = ProtocolNATTraversal
	ack := func(c *nex.Conn, req *nex.Request) *nex.Response { return req.Success(nil) }
	s.Handle(p, "NATTraversal", methodRequestProbeInitiation, "RequestProbeInitiation", s.requestProbeInitiation)
	s.Handle(p, "NATTraversal", methodInitiateProbe, "InitiateProbe", ack)
	s.Handle(p, "NATTraversal", methodRequestProbeInitiationExt, "RequestProbeInitiationExt", s.requestProbeInitiationExt)
	s.Handle(p, "NATTraversal", methodReportNATTraversalResult, "ReportNATTraversalResult", s.reportNATResult(false))
	s.Handle(p, "NATTraversal", methodReportNATTraversalResultDetail, "ReportNATTraversalResultDetail", s.reportNATResult(true))
	s.Handle(p, "NATTraversal", methodReportNATProperties, "ReportNATProperties", s.reportNATProperties)
	s.Handle(p, "NATTraversal", methodGetRelaySignatureKey, "GetRelaySignatureKey", s.getRelaySignatureKey)
}

// requestProbeInitiationExt: List<StationURL> targets | StationURL stationToProbe.
//
// The caller asks the server to have each target probe it back, so both
// NATs open at the same moment. Each target gets an InitiateProbe carrying
// the caller's station.
func (s *Server) requestProbeInitiationExt(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	targets := nex.ReadList(r, (*nex.Reader).StationURL)
	probe := r.StationURL()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	s.initiateProbes(c, targets, probe)
	return req.Success(nil)
}

// requestProbeInitiation: List<StationURL> targets. The older form, with the
// station to probe left implicit: the caller's own public one.
func (s *Server) requestProbeInitiation(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	targets := nex.ReadList(r, (*nex.Reader).StationURL)
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	st := Stations(c)
	if len(st) == 0 {
		return req.Success(nil)
	}
	s.initiateProbes(c, targets, st[len(st)-1])
	return req.Success(nil)
}

// initiateProbes sends InitiateProbe(station) to each target, found by the
// RVCID in its station URL.
//
// Two consoles reaching the server from the same public address are in one
// household: aiming them at that public address needs the router to hairpin,
// which most home routers do not. They get the caller's LAN station instead
// (NextendoNetwork's fix, facts only).
func (s *Server) initiateProbes(c *nex.Conn, targets []*nex.StationURL, probe *nex.StationURL) {
	for _, t := range targets {
		target := s.ConnByID(uint32(t.Int("RVCID")))
		if target == nil || target.PID == 0 || target == c {
			s.log.Debug("nex: probe target not connected", "pid", c.PID, "target", t.String())
			continue
		}
		station := probe
		if sameHousehold(c, target) {
			if lan := lanStation(c); lan != nil {
				station = lan
			}
		}
		station = s.stationFor(c, target, station)
		w := nex.NewWriter()
		w.StationURL(station)
		if err := target.Call(ProtocolNATTraversal, methodInitiateProbe, w.Bytes()); err != nil {
			s.log.Debug("nex: InitiateProbe not delivered", "target", target.PID, "error", err)
		}
	}
}

func sameHousehold(a, b *nex.Conn) bool {
	return a.RemoteIP != "" && a.RemoteIP == b.RemoteIP && !isPrivate(a.RemoteIP)
}

// lanStation is a connection's registered private-address station.
func lanStation(c *nex.Conn) *nex.StationURL {
	for _, st := range Stations(c) {
		if isPrivate(st.Get("address")) {
			return st
		}
	}
	return nil
}

// stationFor is the station a viewer should be given for owner: bridged to
// the observed NAT mapping, then offered to the relay (SetStationRewriter).
func (s *Server) stationFor(owner, viewer *nex.Conn, st *nex.StationURL) *nex.StationURL {
	st = s.bridge(owner, st)
	if rw := s.rewriter.Load(); rw != nil {
		return (*rw)(owner, viewer, st)
	}
	return st
}

// NATObservations looks up the external port the NAT-check responder saw
// mapped for a public IP (internal/natobs).
type NATObservations interface {
	Lookup(ctx context.Context, ip string) (int, bool)
}

// bridge corrects the port of a console's public station.
//
// The public station is built from the connection's address and the port
// the console itself reported -- its LOCAL port, which is only right when
// the router preserves ports. The NAT-check responder saw the port the
// router actually mapped for the Pia socket, and a peer's probe has to aim at
// that one; aimed at the local port it never lands and the match loops back
// to matchmaking. NextendoNetwork closes the same gap the same way (facts
// only).
//
// Only when exactly one connected console sits behind that public IP: the
// observation is keyed by IP, so with two consoles in one household it
// could be either's, and a wrong port is worse than the reported one.
func (s *Server) bridge(owner *nex.Conn, st *nex.StationURL) *nex.StationURL {
	if s.natObs == nil {
		return st
	}
	ip := PublicIP(owner)
	if ip == "" || st.Get("address") != ip || !s.soleOnIP(owner, ip) {
		return st
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	port, ok := s.natObs.Lookup(ctx, ip)
	if !ok || int64(port) == st.Int("port") {
		return st
	}
	c := st.Copy()
	c.SetInt("port", int64(port))
	return c
}

func (s *Server) soleOnIP(owner *nex.Conn, ip string) bool {
	for _, c := range s.Players() {
		if c.PID != owner.PID && c.RemoteIP == ip {
			return false
		}
	}
	return true
}

// StationRewriter can change the station one console is given for another.
type StationRewriter func(owner, viewer *nex.Conn, st *nex.StationURL) *nex.StationURL

// SetStationRewriter installs (or with nil, removes) a StationRewriter.
func (s *Server) SetStationRewriter(rw StationRewriter) {
	if rw == nil {
		s.rewriter.Store(nil)
		return
	}
	s.rewriter.Store(&rw)
}

// reportNATResult: u32 cid | bool result | (Detail: s32 detail) | u32 rtt.
// The only place either console says whether the punch worked, so it is
// kept, not just acknowledged: a lobby that loops back to matchmaking has no
// other trace of why.
func (s *Server) reportNATResult(detail bool) Method {
	return func(c *nex.Conn, req *nex.Request) *nex.Response {
		r := nex.NewReader(req.Body)
		cid := r.U32()
		ok := r.Bool()
		var d int32
		if detail {
			d = r.S32()
		}
		rtt := r.U32()
		if r.Err() != nil {
			return req.Error(nex.ResultInvalidArgument)
		}
		updateNAT(c, func(n *NAT) {
			n.Punches++
			if !ok {
				n.Failures++
			}
		})
		var peer uint64
		if p := s.ConnByID(cid); p != nil {
			peer = p.PID
		}
		s.log.Info("nex: hole-punch result", "pid", c.PID, "peer", peer, "ok", ok, "rtt_ms", rtt, "detail", d)
		s.emitPunch(c, peer, ok)
		return req.Success(nil)
	}
}

// reportNATProperties: u32 mapping | u32 filtering | u32 rtt.
func (s *Server) reportNATProperties(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	mapping, filtering, rtt := r.U32(), r.U32(), r.U32()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	updateNAT(c, func(n *NAT) { n.Mapping, n.Filtering, n.RTT = mapping, filtering, rtt })
	s.log.Info("nex: NAT reported", "pid", c.PID, "class", NATOf(c).Class().String(), "rtt_ms", rtt)
	return req.Success(nil)
}

// getRelaySignatureKey answers "no relay": relay mode 0, the current time,
// an empty address, port 0, address type 0, then the game server id.
//
// The newer Pia's relay handshake; Splatoon 2's direct-mesh Pia is not
// recorded calling it. Answered anyway, because a malformed answer is worse
// than none: NextendoNetwork traced a title's black screen at match start to
// exactly that (facts only). SwitchNet's own relay (relay.go) does not work
// through this: it substitutes stations, which Pia does not need to agree to.
func (s *Server) getRelaySignatureKey(c *nex.Conn, req *nex.Request) *nex.Response {
	w := nex.NewWriter()
	w.S32(0)
	w.DateTime(nex.NewDateTime(s.now()))
	w.String("")
	w.U16(0)
	w.S32(0)
	w.U32(s.title.GameServerID)
	return req.Success(w.Bytes())
}

// PunchHook is told about every hole-punch verdict.
type PunchHook func(c *nex.Conn, peer uint64, ok bool)

// OnPunch registers a PunchHook.
func (s *Server) OnPunch(h PunchHook) {
	s.hooksMu.Lock()
	s.onPunch = append(s.onPunch, h)
	s.hooksMu.Unlock()
}

func (s *Server) emitPunch(c *nex.Conn, peer uint64, ok bool) {
	s.hooksMu.RLock()
	hooks := s.onPunch
	s.hooksMu.RUnlock()
	for _, h := range hooks {
		h(c, peer, ok)
	}
}

// PublicIP is the address a connection reaches the server from, when it is
// a public one.
func PublicIP(c *nex.Conn) string {
	if ip := net.ParseIP(c.RemoteIP); ip == nil || isPrivate(c.RemoteIP) {
		return ""
	}
	return c.RemoteIP
}
