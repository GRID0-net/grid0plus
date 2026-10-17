package npln

// Salmon Run "Again!" backfill: see matchmaking.Queue.OpenBackfill for why a
// crew that presses it is filled here rather than sent back to the queue.

// SetAgainJoined wires Gamesync's AgainJoined before serving.
func (s *Server) SetAgainJoined(joined func(sessionID string, full bool)) {
	s.againJoined.Store(&joined)
}

// GamesyncAgainOpen opens a reopened Salmon Run job's free seats to the
// players searching, for as long as its "Again!" window lasts.
func (s *Server) GamesyncAgainOpen(sessionID string) {
	sess, ok := s.queue.OpenBackfill(sessionID)
	if !ok {
		return
	}
	s.log.Info("npln: Again! lobby open to players searching", "session", sessionID,
		"players", len(sess.Players), "capacity", sess.MaxPlayers)
	s.applyBackfills()
}

// GamesyncAgainLocked closes the seats again once the lobby locked.
func (s *Server) GamesyncAgainLocked(sessionID string) {
	s.queue.CloseBackfill(sessionID)
}

// applyBackfills reports the players the queue put into reopened jobs, so
// the "Again!" window restarts for them or ends when the job is full.
// Called wherever the queue may form matches.
func (s *Server) applyBackfills() {
	for _, fill := range s.queue.TakeBackfills() {
		full := len(fill.Session.Players) >= fill.Session.MaxPlayers
		s.log.Info("npln: players joined an Again! lobby", "session", fill.Session.ID,
			"added", fill.Added, "players", len(fill.Session.Players), "full", full)
		if joined := s.againJoined.Load(); joined != nil {
			(*joined)(fill.Session.ID, full)
		}
	}
}
