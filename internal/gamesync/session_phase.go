package gamesync

import (
	"strings"
	"time"
)

// SessionPhase describes committed client events, not elapsed match time.
// Profile submission is a startup barrier, not proof of the first gameplay
// frame; the dashboard deliberately calls that state "ready", not "playing".
func (s *Server) SessionPhase(scope string, participants int) string {
	s.docs.mu.Lock()
	st := s.docs.stores[scope]
	s.docs.mu.Unlock()
	if st == nil {
		return "connected"
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	var battle string
	var kickoff time.Time
	for name, doc := range st.docs {
		if !strings.HasPrefix(name, refereeRequestPrefix) || !strings.HasSuffix(name, "_"+scope) {
			continue
		}
		at := doc.GetCreateTime().AsTime()
		if battle == "" || at.After(kickoff) {
			battle, kickoff = strings.TrimPrefix(name, refereeRequestPrefix), at
		}
	}
	if battle == "" {
		return "connected"
	}
	profiles := 0
	var reported time.Time
	for name, doc := range st.docs {
		if strings.HasPrefix(name, refereeProfilePrefix+battle+"/User/") {
			profiles++
		}
		if strings.HasPrefix(name, refereeReportPrefix+battle+"/") && doc.GetUpdateTime().AsTime().After(reported) {
			reported = doc.GetUpdateTime().AsTime()
		}
	}
	if !reported.IsZero() {
		if meta := st.docs["docs/__gs/m"]; meta != nil && meta.GetUpdateTime().AsTime().After(reported) {
			f := meta.GetFields().GetFields()
			if f["cp"].GetBooleanValue() && f["ebf"].GetBooleanValue() {
				return "rematch_waiting"
			}
			if !f["cp"].GetBooleanValue() && !f["ebf"].GetBooleanValue() {
				return "rematch_locked"
			}
		}
		return "results"
	}
	if participants > 0 && profiles >= participants {
		return "ready"
	}
	return "starting"
}
