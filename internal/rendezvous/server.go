// Package rendezvous is a NEX game server: the protocols every NEX title
// builds on (Quazal Rendez-Vous, as NEX's own sources call them), assembled
// on internal/nex's transport. A title -- internal/splatoon2 -- configures
// one, registers its own extensions, and serves it.
//
// One Server is one game server, which on the Switch means one hostname
// (g<game server id>-lp1.s.n.srv.nintendo.net) carrying both halves on one
// WebSocket listener:
//
//	virtual port 1: authentication  -- TicketGranting only
//	virtual port 2: secure          -- everything else, as a named player
//
// References: https://github.com/kinnay/NintendoClients/wiki/NEX-Protocols
// and the per-protocol pages linked from each file.
package rendezvous

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/identity"
	"github.com/n-popescu/grid0plus/internal/nex"
)

// Virtual ports. See the package comment.
const (
	AuthPort   = 1
	SecurePort = 2
)

// SecurePID is the principal id of the secure server ("Quazal Rendez-Vous")
// as it appears in the secure station URL and as a ticket's target. 2 is the
// value every 3DS/Wii U server and NextendoNetwork's proven Switch servers use
// (facts only); the client only checks that the ticket's target matches the
// station, and both come from here.
const SecurePID = 2

// Title is what distinguishes one NEX game server from another.
type Title struct {
	// Name is for logs and the dashboard.
	Name string
	// AccessKey is the title's NEX access key (public: it ships in the game).
	AccessKey string
	// GameServerID is the id in the hostname and in GetRelaySignatureKey.
	GameServerID uint32
	// ServerName is the "build name" LoginEx returns.
	ServerName string

	// AuthMinorVersion and SecureMinorVersion are the PRUDP minor versions
	// each port offers. They are separate because a title's proven server can
	// answer them differently -- see internal/splatoon2.
	AuthMinorVersion   uint8
	SecureMinorVersion uint8

	// SecureAddress and SecurePort, when set, make the secure station URL
	// point at them instead of "same place as the authentication server".
	// See SecureStation.
	SecureAddress string
	SecurePort    int

	// PublicStationType is the 'type' Register stamps on the public station
	// it derives for a console. The value follows the title's Pia version.
	PublicStationType int64
	// PublicStationPa adds Pa=<private address> to that station.
	PublicStationPa bool
}

// Method answers one RMC method.
type Method func(c *nex.Conn, req *nex.Request) *nex.Response

// Options configures a Server.
type Options struct {
	Title    Title
	IDTokens *identity.IDTokens
	// Store keeps what outlives a session (see Records), and answers
	// GetPID/GetName.
	Store db.Store
	// NATObservations, when set, corrects the port of a console's public
	// station to the one the NAT-check responder saw mapped (see bridge).
	NATObservations NATObservations
	Log             *slog.Logger
	Now             func() time.Time
	// IdleTimeout is nex.Server's.
	IdleTimeout time.Duration
}

// Server is one NEX game server.
type Server struct {
	title    Title
	log      *slog.Logger
	now      func() time.Time
	idTokens *identity.IDTokens
	store    db.Store
	natObs   NATObservations

	nex       *nex.Server
	secureKey []byte

	mu        sync.RWMutex
	protocols map[uint16]*protocol
	players   map[uint64]*nex.Conn

	hooksMu      sync.RWMutex
	onConnect    []func(*nex.Conn)
	onDisconnect []func(*nex.Conn)
	onCall       []func(c *nex.Conn, req *nex.Request, resp *nex.Response)
	onPunch      []PunchHook

	rewriter atomic.Pointer[StationRewriter]
	mm       *Matchmaker
}

type protocol struct {
	name     string
	methods  map[uint32]Method
	names    map[uint32]string
	fallback Method
}

// New builds a server with TicketGranting on the authentication port and
// SecureConnection on the secure port. Everything else is registered by the
// title.
func New(opts Options) *Server {
	s := &Server{
		title:     opts.Title,
		log:       opts.Log,
		now:       opts.Now,
		idTokens:  opts.IDTokens,
		store:     opts.Store,
		natObs:    opts.NATObservations,
		protocols: map[uint16]*protocol{},
		players:   map[uint64]*nex.Conn{},
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.log = s.log.With("title", opts.Title.Name)
	if s.now == nil {
		s.now = time.Now
	}

	// The secure server's own key: random per process, never configured.
	// Only this process issues tickets for it and only this process checks
	// them, so there is nothing to share -- and a ticket from before a restart
	// is exactly as good as the two-minute lifetime already makes it.
	s.secureKey = make([]byte, 16)
	if _, err := rand.Read(s.secureKey); err != nil {
		panic(err)
	}

	s.nex = &nex.Server{
		AccessKey:         opts.Title.AccessKey,
		Log:               s.log,
		IdleTimeout:       opts.IdleTimeout,
		TrustForwardedFor: nex.Loopback,
		Ports: map[uint8]*nex.Port{
			AuthPort: {
				MinorVersion: opts.Title.AuthMinorVersion,
				Handler:      nex.HandlerFunc(s.serveAuth),
			},
			SecurePort: {
				MinorVersion: opts.Title.SecureMinorVersion,
				Accept:       nex.TicketAcceptor(s.secureKey, s.now),
				Handler:      nex.HandlerFunc(s.serveSecure),
				OnConnect:    s.playerConnected,
				OnClose:      s.playerDisconnected,
			},
		},
	}
	s.registerSecureConnection()
	s.registerUtility()
	s.registerNATTraversal()
	s.registerMatchmaking()
	s.registerRanking()
	s.registerPresence()
	return s
}

// exists reports whether pid is a live user.
func (s *Server) exists(pid uint64) bool {
	if s.store == nil || pid == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u, err := s.store.GetUser(ctx, UserIDForPID(pid))
	return err == nil && !u.Deleted
}

// ServeHTTP serves the WebSocket endpoint.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.nex.ServeHTTP(w, r) }

// Title returns the title configuration.
func (s *Server) Title() Title { return s.title }

// Log is the server's logger.
func (s *Server) Log() *slog.Logger { return s.log }

// Now is the server's clock.
func (s *Server) Now() time.Time { return s.now() }

// Handle registers a method on the secure port, replacing any earlier one.
// name is what logs and the dashboard call it.
func (s *Server) Handle(protocolID uint16, protocolName string, methodID uint32, methodName string, m Method) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.protocols[protocolID]
	if p == nil {
		p = &protocol{name: protocolName, methods: map[uint32]Method{}, names: map[uint32]string{}}
		s.protocols[protocolID] = p
	}
	p.methods[methodID] = m
	p.names[methodID] = methodName
}

// HandleFallback answers every method of a protocol that has no handler of
// its own. For a title whose proven server is known to answer a whole
// protocol a certain way; see internal/splatoon2's DataStore.
func (s *Server) HandleFallback(protocolID uint16, protocolName string, m Method) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.protocols[protocolID]
	if p == nil {
		p = &protocol{name: protocolName, methods: map[uint32]Method{}, names: map[uint32]string{}}
		s.protocols[protocolID] = p
	}
	p.fallback = m
}

// MethodName is "Protocol.Method" for logs and the dashboard, or the numbers
// when nothing is registered.
func (s *Server) MethodName(protocolID uint16, methodID uint32) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p := s.protocols[protocolID]; p != nil {
		if n, ok := p.names[methodID]; ok {
			return p.name + "." + n
		}
		return fmt.Sprintf("%s.%d", p.name, methodID)
	}
	return fmt.Sprintf("0x%X.%d", protocolID, methodID)
}

// Methods lists every registered method, for the dashboard and the README
// test that keeps the documentation honest.
func (s *Server) Methods() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, p := range s.protocols {
		for _, n := range p.names {
			out = append(out, p.name+"."+n)
		}
	}
	sort.Strings(out)
	return out
}

// OnConnect registers a hook run when a player's secure connection is
// established.
func (s *Server) OnConnect(f func(*nex.Conn)) {
	s.hooksMu.Lock()
	s.onConnect = append(s.onConnect, f)
	s.hooksMu.Unlock()
}

// OnDisconnect registers a hook run when it ends -- including when a newer
// connection for the same player replaced it.
func (s *Server) OnDisconnect(f func(*nex.Conn)) {
	s.hooksMu.Lock()
	s.onDisconnect = append(s.onDisconnect, f)
	s.hooksMu.Unlock()
}

// OnCall registers a hook run after every secure call is answered (resp is
// nil for a call answered later or never).
func (s *Server) OnCall(f func(c *nex.Conn, req *nex.Request, resp *nex.Response)) {
	s.hooksMu.Lock()
	s.onCall = append(s.onCall, f)
	s.hooksMu.Unlock()
}

// Player returns a player's live secure connection.
func (s *Server) Player(pid uint64) *nex.Conn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.players[pid]
}

// Players returns every player's live secure connection.
func (s *Server) Players() []*nex.Conn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*nex.Conn, 0, len(s.players))
	for _, c := range s.players {
		out = append(out, c)
	}
	return out
}

// ConnByID finds any live connection by its connection id (RVCID).
func (s *Server) ConnByID(id uint32) *nex.Conn { return s.nex.Conn(id) }

// playerConnected registers a secure connection as its player's.
//
// One place at a time, newest wins. A console that lost its connection opens
// a new one before the old one times out, so refusing the newcomer would lock
// a player out of their own session for the length of the timeout -- which is
// what NextendoNetwork's players reported ("this account is already playing
// elsewhere") until they added a reaper (facts only). The identity is not in
// question either way: both connections were admitted on a ticket for this
// PID.
func (s *Server) playerConnected(c *nex.Conn) error {
	s.mu.Lock()
	old := s.players[c.PID]
	s.players[c.PID] = c
	s.mu.Unlock()
	if old != nil {
		s.log.Info("nex: newer connection replaces the player's previous one", "pid", c.PID, "old", old.ID, "new", c.ID)
		old.Close()
	}
	s.log.Info("nex: player connected", "pid", c.PID, "conn", c.ID, "remote", c.RemoteIP)

	s.hooksMu.RLock()
	hooks := append([]func(*nex.Conn){}, s.onConnect...)
	s.hooksMu.RUnlock()
	for _, h := range hooks {
		h(c)
	}
	return nil
}

func (s *Server) playerDisconnected(c *nex.Conn) {
	s.mu.Lock()
	if s.players[c.PID] == c {
		delete(s.players, c.PID)
	}
	s.mu.Unlock()
	s.log.Info("nex: player disconnected", "pid", c.PID, "conn", c.ID)

	s.hooksMu.RLock()
	hooks := append([]func(*nex.Conn){}, s.onDisconnect...)
	s.hooksMu.RUnlock()
	for _, h := range hooks {
		h(c)
	}
}

func (s *Server) serveSecure(c *nex.Conn, req *nex.Request) *nex.Response {
	s.mu.RLock()
	var m Method
	if p := s.protocols[req.Protocol]; p != nil {
		m = p.methods[req.Method]
		if m == nil {
			m = p.fallback
		}
	}
	s.mu.RUnlock()

	var resp *nex.Response
	if m == nil {
		// Logged at warn: this is the to-do list. Every entry is a call a
		// console made that this server does not answer.
		s.log.Warn("nex: method not implemented", "pid", c.PID, "method", s.MethodName(req.Protocol, req.Method),
			"body_len", len(req.Body))
		resp = req.Error(nex.ResultNotImplemented)
	} else {
		resp = m(c, req)
		if resp != nil && !resp.OK() {
			s.log.Info("nex: call answered with an error", "pid", c.PID,
				"method", s.MethodName(req.Protocol, req.Method), "result", fmt.Sprintf("%#x", resp.Result))
		} else {
			s.log.Debug("nex: call", "pid", c.PID, "method", s.MethodName(req.Protocol, req.Method))
		}
	}

	s.hooksMu.RLock()
	hooks := s.onCall
	s.hooksMu.RUnlock()
	for _, h := range hooks {
		h(c, req, resp)
	}
	return resp
}

// UserIDForPID is the BAAS user id a PID stands for. On GRID0+ a player's
// NEX PID IS their user id: the console logs in with the user id as a
// decimal username (NintendoClients' switch examples: str(int(user_id, 16))),
// so using it unchanged means there is no mapping to store, lose, or get
// wrong -- and no second identifier a player could be confused with.
func UserIDForPID(pid uint64) string { return fmt.Sprintf("%016x", pid) }

// PIDForUserID is UserIDForPID's inverse.
func PIDForUserID(userID string) (uint64, error) {
	if len(userID) != 16 {
		return 0, fmt.Errorf("rendezvous: user id %q is not 16 hex digits", userID)
	}
	return strconv.ParseUint(userID, 16, 64)
}
