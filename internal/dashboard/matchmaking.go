package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The matchmaking controls.
//
// The queue lives in cmd/npln, which is a DIFFERENT PROCESS from this one, so
// everything here goes over npln's admin API rather than touching a queue
// directly. That is also why the panel degrades honestly when it is not
// configured: an operator who has not set npln.admin_listen and npln.admin_token
// sees the panel say so, rather than an empty queue that looks like nobody is
// playing.

// matchmakingTimeout bounds a call to npln.
//
// Short. These are in-memory operations on a queue behind a loopback or LAN
// address; anything slower means npln is gone, and the page should say that
// promptly rather than hanging while the operator presses the button again.
const matchmakingTimeout = 5 * time.Second

// matchmakingClient talks to npln's admin API.
type matchmakingClient struct {
	base  string
	token string
	http  *http.Client
}

func newMatchmakingClient(address, token string) *matchmakingClient {
	if address == "" || token == "" {
		return nil
	}

	// An operator is as likely to write "127.0.0.1:8088" as a URL, and a bare
	// host:port is what every other listen address in the configuration looks
	// like.
	base := strings.TrimSuffix(strings.TrimSpace(address), "/")
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}

	return &matchmakingClient{
		base:  base,
		token: token,
		http:  &http.Client{Timeout: matchmakingTimeout},
	}
}

// call performs one admin request and returns the decoded body.
//
// Errors carry npln's own message rather than a status code: "nobody is queuing
// for turf_war" is the whole explanation, and replacing it with "409" would make
// the operator go and read a log to find out what the page already knew.
func (c *matchmakingClient) call(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the game server is not answering at %s", c.base)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(raw))
		if resp.StatusCode == http.StatusUnauthorized {
			message = "the game server rejected the admin token; " +
				"npln.admin_token and dashboard.npln_admin_token must match"
		}
		if message == "" {
			message = resp.Status
		}
		return nil, fmt.Errorf("%s", message)
	}

	return json.RawMessage(raw), nil
}

// ---- routes ---------------------------------------------------------------

// handleMatchmaking returns the queue, or says why it cannot.
func (s *Server) handleMatchmaking(w http.ResponseWriter, r *http.Request) {
	if s.matchmaking == nil {
		// 200 with a shape the page can render, not an error: not configuring
		// the controls is a valid deployment, and a red failure box for it would
		// train the operator to ignore red boxes.
		writeJSON(w, map[string]any{
			"configured": false,
			"reason": "Set npln.admin_listen and npln.admin_token on the game server, " +
				"and dashboard.npln_admin_url and dashboard.npln_admin_token here.",
		})
		return
	}

	body, err := s.matchmaking.call(r.Context(), http.MethodGet, "/matchmaking", nil)
	if err != nil {
		s.writeMatchmakingError(w, err)
		return
	}
	s.writeMatchmaking(w, body)
}

// handleForceStart starts a match in a mode with whoever is waiting.
func (s *Server) handleForceStart(w http.ResponseWriter, r *http.Request) {
	s.matchmakingAction(w, r, http.MethodPost,
		"/matchmaking/modes/"+url.PathEscape(r.PathValue("mode"))+"/start", nil)
}

// handleSetModeSize changes how many players a mode takes.
func (s *Server) handleSetModeSize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Players int `json:"players"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	s.matchmakingAction(w, r, http.MethodPost,
		"/matchmaking/modes/"+url.PathEscape(r.PathValue("mode"))+"/size", req)
}

// handleCancelTicket drops one ticket out of the queue.
func (s *Server) handleCancelTicket(w http.ResponseWriter, r *http.Request) {
	s.matchmakingAction(w, r, http.MethodPost,
		"/matchmaking/tickets/"+url.PathEscape(r.PathValue("id"))+"/cancel", nil)
}

func (s *Server) matchmakingAction(
	w http.ResponseWriter, r *http.Request, method, path string, body any,
) {
	if s.matchmaking == nil {
		http.Error(w, "the matchmaking controls are not configured", http.StatusServiceUnavailable)
		return
	}

	got, err := s.matchmaking.call(r.Context(), method, path, body)
	if err != nil {
		s.writeMatchmakingError(w, err)
		return
	}
	s.writeMatchmaking(w, got)
}

// writeMatchmaking passes npln's body through with `configured` added.
//
// Re-encoded rather than proxied verbatim so the page has one shape to render
// whether or not the controls are set up.
func (s *Server) writeMatchmaking(w http.ResponseWriter, body json.RawMessage) {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		s.fail(w, "read the matchmaking queue", err)
		return
	}
	decoded["configured"] = true
	writeJSON(w, decoded)
}

// writeMatchmakingError surfaces npln's own message.
//
// 502 rather than 500: the dashboard is fine, the thing behind it is not, and
// that distinction is the first thing an operator needs.
func (s *Server) writeMatchmakingError(w http.ResponseWriter, err error) {
	s.log.Warn("dashboard: matchmaking control failed", "error", err)
	http.Error(w, err.Error(), http.StatusBadGateway)
}
