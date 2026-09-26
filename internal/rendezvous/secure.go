package rendezvous

import (
	"net"
	"sync"

	"github.com/n-popescu/switchnet/internal/nex"
)

// SecureConnection (protocol 11): where a console registers the addresses
// its peers can reach it at. https://github.com/kinnay/NintendoClients/wiki/Secure-Protocol
const ProtocolSecureConnection = 0x0B

const (
	methodRegister              = 1
	methodRequestConnectionData = 2
	methodRequestURLs           = 3
	methodRegisterEx            = 4
	methodTestConnectivity      = 5
	methodUpdateURLs            = 6
	methodReplaceURL            = 7
	methodSendReport            = 8
)

func (s *Server) registerSecureConnection() {
	const p = ProtocolSecureConnection
	s.Handle(p, "SecureConnection", methodRegister, "Register", s.register)
	s.Handle(p, "SecureConnection", methodRegisterEx, "RegisterEx", s.register)
	s.Handle(p, "SecureConnection", methodRequestConnectionData, "RequestConnectionData", s.requestConnectionData)
	s.Handle(p, "SecureConnection", methodRequestURLs, "RequestURLs", s.requestURLs)
	s.Handle(p, "SecureConnection", methodTestConnectivity, "TestConnectivity",
		// No parameters, no result: answering at all is the test.
		func(c *nex.Conn, req *nex.Request) *nex.Response { return req.Success(nil) })
	s.Handle(p, "SecureConnection", methodUpdateURLs, "UpdateURLs", s.updateURLs)
	s.Handle(p, "SecureConnection", methodReplaceURL, "ReplaceURL", s.replaceURL)
	s.Handle(p, "SecureConnection", methodSendReport, "SendReport", s.sendReport)
}

// stationsKey holds a connection's registered stations.
type stationsKey struct{}

type stationSet struct {
	mu   sync.Mutex
	urls []*nex.StationURL
}

func stationsOf(c *nex.Conn) *stationSet {
	if v, ok := c.Value(stationsKey{}).(*stationSet); ok {
		return v
	}
	set := &stationSet{}
	c.SetValue(stationsKey{}, set)
	return set
}

// Stations returns copies of a connection's registered stations, in order.
func Stations(c *nex.Conn) []*nex.StationURL {
	set := stationsOf(c)
	set.mu.Lock()
	defer set.mu.Unlock()
	out := make([]*nex.StationURL, len(set.urls))
	for i, u := range set.urls {
		out[i] = u.Copy()
	}
	return out
}

func setStations(c *nex.Conn, urls []*nex.StationURL) {
	set := stationsOf(c)
	set.mu.Lock()
	set.urls = urls
	set.mu.Unlock()
}

// register answers Register and RegisterEx.
//
//	request:  List<StationURL> | (RegisterEx) AnyDataHolder login data
//	response: Result | u32 connection id | StationURL public
//
// The console reports only what it knows about itself, which behind a router
// is its LAN address. The server knows one more thing -- the address the
// connection arrived from -- and adds a public station built from it, so that
// a peer elsewhere has something to aim at.
func (s *Server) register(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	urls := nex.ReadList(r, (*nex.Reader).StationURL)
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}

	local, public := s.splitStations(c, urls)

	for _, u := range []*nex.StationURL{local, public} {
		u.SetUint("PID", c.PID)
		u.SetInt("RVCID", int64(c.ID))
	}
	public.SetInt("type", s.title.PublicStationType)
	if s.title.PublicStationPa {
		public.Set("Pa", local.Get("address"))
	}
	setStations(c, []*nex.StationURL{local, public})
	s.log.Info("nex: registered stations", "pid", c.PID, "local", local.String(), "public", public.String())

	w := nex.NewWriter()
	w.Result(nex.ResultSuccess)
	w.U32(c.ID)
	w.StationURL(public)
	return req.Success(w.Bytes())
}

// splitStations picks the console's LAN station and builds its public one.
//
// Chosen by address, not by the 'type' flags: NextendoNetwork records (facts
// only) that a Switch commonly reports its stations without the public flag
// set, so the flags do not say which is which.
//
// The derived public station is a copy of the LAN one -- it has to keep
// every parameter the console sent (sid, Pl, Tpt, natm, natf, ...) or, per
// the same source, Pia does not recognise its own endpoint and gives up with
// 2618-0562 -- with the address replaced by the one the connection came from.
// The port is kept: it is Pia's UDP port, and the WebSocket's TCP port would
// be meaningless to a peer. Whether the router preserved it is exactly what
// the NAT check and the later ReplaceURL establish.
func (s *Server) splitStations(c *nex.Conn, urls []*nex.StationURL) (local, public *nex.StationURL) {
	for _, u := range urls {
		addr := u.Get("address")
		if addr == "" {
			continue
		}
		if isPrivate(addr) {
			if local == nil {
				local = u
			}
		} else if public == nil {
			public = u
		}
	}
	if local == nil && len(urls) > 0 {
		local = urls[0]
	}
	if local == nil {
		local = nex.NewStationURL("prudp")
		local.Set("address", c.RemoteIP)
	}
	if public == nil || public == local {
		public = local.Copy()
		if c.RemoteIP != "" {
			public.Set("address", c.RemoteIP)
		}
	}
	return local, public
}

// cgnat is RFC 6598 shared address space: a console behind carrier-grade NAT
// reports one of these as its LAN address, and it is no more reachable by a
// peer than an RFC 1918 one.
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isPrivate(addr string) bool {
	ip := net.ParseIP(addr)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip))
}

func (s *Server) target(r *nex.Reader) *nex.Conn {
	cid := r.U32()
	pid := r.PID()
	if r.Err() != nil {
		return nil
	}
	if c := s.ConnByID(cid); c != nil && c.PID == pid {
		return c
	}
	if cid == 0 {
		return s.Player(pid)
	}
	return nil
}

// requestConnectionData: u32 cid | PID pid -> Bool | List<ConnectionData{StationURL, u32 connection id}>
func (s *Server) requestConnectionData(c *nex.Conn, req *nex.Request) *nex.Response {
	t := s.target(nex.NewReader(req.Body))
	w := nex.NewWriter()
	w.Bool(t != nil)
	var urls []*nex.StationURL
	if t != nil {
		urls = Stations(t)
	}
	nex.WriteList(w, urls, func(w *nex.Writer, u *nex.StationURL) {
		w.Struct(0, func(w *nex.Writer) {
			w.StationURL(u)
			w.U32(t.ID)
		})
	})
	return req.Success(w.Bytes())
}

// requestURLs: u32 cid | PID pid -> Bool | List<StationURL>
func (s *Server) requestURLs(c *nex.Conn, req *nex.Request) *nex.Response {
	t := s.target(nex.NewReader(req.Body))
	w := nex.NewWriter()
	w.Bool(t != nil)
	var urls []*nex.StationURL
	if t != nil {
		urls = Stations(t)
	}
	nex.WriteList(w, urls, (*nex.Writer).StationURL)
	return req.Success(w.Bytes())
}

// updateURLs replaces the registered stations wholesale.
func (s *Server) updateURLs(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	urls := nex.ReadList(r, (*nex.Reader).StationURL)
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	for _, u := range urls {
		u.SetUint("PID", c.PID)
	}
	setStations(c, urls)
	return req.Success(nil)
}

// replaceURL: StationURL old | StationURL new.
//
// After its NAT check the console re-reports a station carrying what it has
// just learned -- its mapped port and the connection id -- and a joiner's
// probe needs exactly that, so it must be kept. NextendoNetwork records (facts
// only) that dropping it broke every friend-join with a communication error.
//
// Matched by address rather than by comparing the old URL: the console's
// idea of its old URL and the server's (which added PID and RVCID) differ.
// The new station takes the old one's place; one reporting an address not
// yet registered is appended.
func (s *Server) replaceURL(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	_ = r.StationURL()
	fresh := r.StationURL()
	if r.Err() != nil {
		return req.Error(nex.ResultInvalidArgument)
	}
	fresh.SetUint("PID", c.PID)
	if !fresh.Has("RVCID") {
		fresh.SetInt("RVCID", int64(c.ID))
	}

	set := stationsOf(c)
	set.mu.Lock()
	replaced := false
	for i, u := range set.urls {
		if u.Get("address") == fresh.Get("address") {
			set.urls[i] = fresh
			replaced = true
			break
		}
	}
	if !replaced {
		set.urls = append(set.urls, fresh)
	}
	set.mu.Unlock()
	s.log.Info("nex: station replaced", "pid", c.PID, "station", fresh.String())
	return req.Success(nil)
}

// sendReport: u32 report id | qBuffer data. A diagnostic the console sends;
// logged for whoever is reading, and acknowledged.
func (s *Server) sendReport(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	id := r.U32()
	data := r.QBuffer()
	s.log.Info("nex: console report", "pid", c.PID, "report", id, "len", len(data))
	return req.Success(nil)
}
