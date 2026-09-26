package dashboard

import (
	"encoding/json"
	"net/http"
	"net/url"
)

// The Splatoon 2 panel.
//
// Splatoon 2's game server is cmd/nex, another process, so like the
// matchmaking controls everything here goes over that process's admin API
// and the panel says plainly when it is not configured.

func newSplatoon2Client(address, token string) *adminClient {
	return newAdminClient(address, token,
		"the NEX server rejected the admin token; "+
			"nex.admin_token and dashboard.nex_admin_token must match")
}

// handleSplatoon2 returns the NEX server's status, or says why it cannot.
func (s *Server) handleSplatoon2(w http.ResponseWriter, r *http.Request) {
	if s.splatoon2 == nil {
		writeJSON(w, map[string]any{
			"configured": false,
			"reason": "Set nex.admin_listen and nex.admin_token on the NEX server, " +
				"and dashboard.nex_admin_url and dashboard.nex_admin_token here.",
		})
		return
	}
	s.splatoon2Action(w, r, http.MethodGet, "/splatoon2", nil)
}

// handleSplatoon2GearOrder puts a piece of gear on order with Annie for a
// player: what the SplatNet 2 app did.
func (s *Server) handleSplatoon2GearOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind  int32 `json:"kind"`
		ID    int32 `json:"id"`
		Skill int32 `json:"skill"`
		Price int32 `json:"price"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	s.splatoon2Action(w, r, http.MethodPost, splatoon2PlayerPath(r, "gear-order"), req)
}

func (s *Server) handleSplatoon2CancelGearOrder(w http.ResponseWriter, r *http.Request) {
	s.splatoon2Action(w, r, http.MethodDelete, splatoon2PlayerPath(r, "gear-order"), nil)
}

func (s *Server) handleSplatoon2Disconnect(w http.ResponseWriter, r *http.Request) {
	s.splatoon2Action(w, r, http.MethodPost, splatoon2PlayerPath(r, "disconnect"), nil)
}

func splatoon2PlayerPath(r *http.Request, action string) string {
	return "/splatoon2/players/" + url.PathEscape(r.PathValue("id")) + "/" + action
}

func (s *Server) splatoon2Action(w http.ResponseWriter, r *http.Request, method, path string, body any) {
	if s.splatoon2 == nil {
		http.Error(w, "the Splatoon 2 panel is not configured", http.StatusServiceUnavailable)
		return
	}
	got, err := s.splatoon2.call(r.Context(), method, path, body)
	if err != nil {
		s.log.Warn("dashboard: splatoon 2 control failed", "error", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		s.fail(w, "read the Splatoon 2 status", err)
		return
	}
	decoded["configured"] = true
	writeJSON(w, decoded)
}
