package gamesync

import "time"

// memberGoneGrace is how long a player may be without a watch stream on a
// game session before they are taken to have left it.
//
// The game never says it is leaving: a console that quits a room, or loses
// its connection, just closes its KeepUserSession stream, and without acting
// on that the leaver stays in the session's players. The next friend to join
// is then told to connect to a console that is gone, and fails with
// 2318-1201 (grid0, 2026-10-17: a Salmon Run room kept 142f3fed after its
// stream closed at 16:43:43, and 26baf76 could not join it twice).
//
// A stream does not survive everything a member does: on grid0 a non-host
// member's room stream closed when the job ended and came back 23 and 32
// seconds later. Every such return was preceded, in the same second, by a
// JoinGameSession ("npln: joined room"), so dropping the member in between
// costs nothing: the join puts them back, in the seat they had (see
// matchmaking's Session.Seats). What the room shows meanwhile is one player
// fewer, which is what it is.
//
// Ten seconds rather than zero so a stream the console re-opens straight away
// does not drop it; short enough that a friend joining right after somebody
// left does not hit the ghost.
var memberGoneGrace = 10 * time.Second

// awaitDeparture starts the grace period for a user session whose last stream
// just closed. Called with cleanupMu held.
func (s *Server) awaitDeparture(caller *session) {
	if s.memberGone == nil || caller.details == nil || caller.details.SessionID == "" {
		return
	}
	name := caller.userSession
	if t := s.departures[name]; t != nil {
		t.Stop()
	}
	sessionID, userID, grace := caller.details.SessionID, caller.userID, memberGoneGrace
	var timer *time.Timer
	timer = time.AfterFunc(grace, func() {
		s.cleanupMu.Lock()
		// Stop cannot retract a callback already waiting for this mutex:
		// only the latest timer for the name may act, and only if no stream
		// came back in the meantime.
		if s.departures[name] != timer {
			s.cleanupMu.Unlock()
			return
		}
		delete(s.departures, name)
		back := s.cleanups[name] != nil && s.cleanups[name].streams > 0
		s.cleanupMu.Unlock()
		if back {
			return
		}
		s.log.Info("gamesync: player gone from the session", "session", sessionID, "user", userID,
			"grace", grace)
		s.memberGone(sessionID, userID)
	})
	s.departures[name] = timer
}

// cancelDeparture keeps a player who re-opened a stream in time. Called with
// cleanupMu held.
func (s *Server) cancelDeparture(caller *session) {
	if t := s.departures[caller.userSession]; t != nil {
		t.Stop()
		delete(s.departures, caller.userSession)
	}
}
