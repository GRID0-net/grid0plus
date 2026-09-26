package splatoon2

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/n-popescu/switchnet/internal/nex"
	"github.com/n-popescu/switchnet/internal/rendezvous"
)

// The operator API the dashboard's Splatoon 2 panel reads: who is connected,
// the lobbies, what the consoles are calling, the relay. Like npln's, a
// separate listener that no console reaches, served only with a token.

// callLog keeps the recent calls and a count per method.
type callLog struct {
	mu     sync.Mutex
	recent []CallEntry
	counts map[string]int64
}

// CallEntry is one call a console made.
type CallEntry struct {
	At     time.Time `json:"at"`
	PID    uint64    `json:"pid"`
	Method string    `json:"method"`
	OK     bool      `json:"ok"`
}

const recentCalls = 200

func (s *Server) noteCall(c *nex.Conn, req *nex.Request, resp *nex.Response) {
	e := CallEntry{At: s.Now(), PID: c.PID, Method: s.MethodName(req.Protocol, req.Method), OK: resp == nil || resp.OK()}
	s.calls.mu.Lock()
	s.calls.recent = append(s.calls.recent, e)
	if len(s.calls.recent) > recentCalls {
		s.calls.recent = s.calls.recent[len(s.calls.recent)-recentCalls:]
	}
	s.calls.counts[e.Method]++
	s.calls.mu.Unlock()
}

type adminPlayer struct {
	PID         uint64     `json:"pid"`
	UserID      string     `json:"userId"`
	Nickname    string     `json:"nickname"`
	RemoteIP    string     `json:"remoteIp"`
	ConnectedAt time.Time  `json:"connectedAt"`
	LastSeen    time.Time  `json:"lastSeen"`
	NAT         string     `json:"nat"`
	RTT         uint32     `json:"rttMs"`
	Punches     uint32     `json:"punches"`
	Failures    uint32     `json:"punchFailures"`
	Gathering   uint32     `json:"gathering,omitempty"`
	Order       *GearOrder `json:"gearOrder,omitempty"`
}

type adminMember struct {
	PID      uint64 `json:"pid"`
	UserID   string `json:"userId"`
	Nickname string `json:"nickname"`
}

type adminGathering struct {
	ID        uint32        `json:"id"`
	Owner     string        `json:"owner"`
	Host      string        `json:"host"`
	Mode      string        `json:"mode"`
	Players   []adminMember `json:"players"`
	Max       uint16        `json:"max"`
	Open      bool          `json:"open"`
	Private   bool          `json:"private"`
	Started   bool          `json:"started"`
	CreatedAt time.Time     `json:"createdAt"`
}

type adminMethod struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type adminResult struct {
	UserID string    `json:"userId"`
	Method string    `json:"method"`
	Mode   uint32    `json:"gameMode"`
	Rule   int32     `json:"rule"`
	Stage  int32     `json:"stage"`
	Result uint8     `json:"result"`
	At     time.Time `json:"at"`
}

// Status is what the dashboard shows.
type Status struct {
	Players    []adminPlayer          `json:"players"`
	Gatherings []adminGathering       `json:"gatherings"`
	Calls      []CallEntry            `json:"calls"`
	Methods    []adminMethod          `json:"methods"`
	Results    []adminResult          `json:"results"`
	Relay      *rendezvous.RelayStats `json:"relay,omitempty"`
	Uptime     int                    `json:"uptimeSeconds"`
}

// Status snapshots the game server.
func (s *Server) Status() Status {
	var st Status
	names := map[uint64]string{}
	name := func(pid uint64) string {
		if n, ok := names[pid]; ok {
			return n
		}
		n := s.Nickname(pid)
		names[pid] = n
		return n
	}
	for _, c := range s.Players() {
		n := rendezvous.NATOf(c)
		p := adminPlayer{PID: c.PID, UserID: rendezvous.UserIDForPID(c.PID), Nickname: name(c.PID),
			RemoteIP: c.RemoteIP, ConnectedAt: c.ConnectedAt, LastSeen: c.LastSeen(),
			NAT: n.Class().String(), RTT: n.RTT, Punches: n.Punches, Failures: n.Failures}
		if g, ok := s.Matchmaker().GatheringOf(c.PID); ok {
			p.Gathering = g.ID
		}
		if o, ok := s.GearOrderOf(c.PID); ok {
			p.Order = &o
		}
		st.Players = append(st.Players, p)
	}
	sort.Slice(st.Players, func(i, j int) bool { return st.Players[i].ConnectedAt.Before(st.Players[j].ConnectedAt) })

	for _, g := range s.Matchmaker().Gatherings() {
		ag := adminGathering{ID: g.ID, Owner: rendezvous.UserIDForPID(g.Owner), Host: rendezvous.UserIDForPID(g.Host),
			Mode: GameModeName(g.GameMode), Max: g.Max, Open: g.Open, Private: g.Private, Started: g.Started,
			CreatedAt: g.Created}
		for _, p := range g.Participants {
			ag.Players = append(ag.Players, adminMember{PID: p, UserID: rendezvous.UserIDForPID(p), Nickname: name(p)})
		}
		st.Gatherings = append(st.Gatherings, ag)
	}

	s.calls.mu.Lock()
	st.Calls = append([]CallEntry(nil), s.calls.recent...)
	for n, c := range s.calls.counts {
		st.Methods = append(st.Methods, adminMethod{Name: n, Count: c})
	}
	s.calls.mu.Unlock()
	for i, j := 0, len(st.Calls)-1; i < j; i, j = i+1, j-1 {
		st.Calls[i], st.Calls[j] = st.Calls[j], st.Calls[i]
	}
	sort.Slice(st.Methods, func(i, j int) bool {
		if st.Methods[i].Count != st.Methods[j].Count {
			return st.Methods[i].Count > st.Methods[j].Count
		}
		return st.Methods[i].Name < st.Methods[j].Name
	})

	for _, r := range s.RecentResults(25) {
		st.Results = append(st.Results, adminResult{UserID: rendezvous.UserIDForPID(r.PID), Method: r.Method,
			Mode: r.GameMode, Rule: r.Rule, Stage: r.Stage, Result: r.Result, At: time.Unix(r.At, 0).UTC()})
	}
	if s.relay != nil {
		rs := s.relay.Stats()
		st.Relay = &rs
	}
	st.Uptime = int(s.Now().Sub(s.started).Seconds())
	return st
}

// AdminHandler serves the operator API, or is nil without a token: this API
// can disconnect players and place orders in their name, so it is not served
// to whoever can reach the port.
func (s *Server) AdminHandler(token string) http.Handler {
	if token == "" {
		return nil
	}
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or wrong admin token"})
				return
			}
			h(w, r)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /splatoon2", guard(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Status())
	}))
	mux.HandleFunc("POST /splatoon2/players/{id}/gear-order", guard(s.adminPlaceOrder))
	mux.HandleFunc("DELETE /splatoon2/players/{id}/gear-order", guard(s.adminCancelOrder))
	mux.HandleFunc("POST /splatoon2/players/{id}/disconnect", guard(s.adminDisconnect))
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func pathPID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	pid, err := rendezvous.PIDForUserID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a user id"})
		return 0, false
	}
	return pid, true
}

// adminPlaceOrder: {"kind", "id", "skill", "price"}. What the SplatNet 2 app
// did for a player: put a piece of gear on order with Annie. The player
// picks it up in the game.
func (s *Server) adminPlaceOrder(w http.ResponseWriter, r *http.Request) {
	pid, ok := pathPID(w, r)
	if !ok {
		return
	}
	var o GearOrder
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&o); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {kind, id, skill, price}"})
		return
	}
	if err := s.PlaceGearOrder(pid, o); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.log.Info("s2: gear order placed by the operator", "pid", pid, "gear", o.ID)
	writeJSON(w, http.StatusOK, s.Status())
}

func (s *Server) adminCancelOrder(w http.ResponseWriter, r *http.Request) {
	pid, ok := pathPID(w, r)
	if !ok {
		return
	}
	_ = s.Records().Delete(kindGearOrder, uint64Key(pid))
	writeJSON(w, http.StatusOK, s.Status())
}

// adminDisconnect ends a player's connection: the console notices at once
// and leaves its lobby, which is how an operator clears a stuck lobby.
func (s *Server) adminDisconnect(w http.ResponseWriter, r *http.Request) {
	pid, ok := pathPID(w, r)
	if !ok {
		return
	}
	c := s.Player(pid)
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that player is not connected"})
		return
	}
	c.Close()
	s.log.Info("s2: player disconnected by the operator", "pid", pid)
	writeJSON(w, http.StatusOK, s.Status())
}
