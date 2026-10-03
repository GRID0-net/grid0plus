package gamesync

import (
	"sort"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

// A deferment is a named batch the console asks Gamesync to run if its user
// session goes away. The game registers room cleanup before displaying the
// room; refusing the registration makes room creation fail immediately.
func validateDeferments(ops []*syncpb.DefermentOperation) error {
	for _, op := range ops {
		switch t := op.GetOperationType().(type) {
		case *syncpb.DefermentOperation_UpdateDeferment:
			if t.UpdateDeferment.GetDeferment().GetName() == "" {
				return status.Error(codes.InvalidArgument, "deferment has no name")
			}
		case *syncpb.DefermentOperation_DeleteDeferment:
			if t.DeleteDeferment.GetName() == "" {
				return status.Error(codes.InvalidArgument, "deferment has no name")
			}
		default:
			return status.Error(codes.InvalidArgument, "unknown deferment operation")
		}
	}
	return nil
}

// userSessionCleanup is a user session's registered cleanup and its open
// KeepUserSession streams.
//
// Keyed by the user-session name rather than by the gamesync token. A
// refreshed token is a new session object for the same user session, and so
// is a rejoin: user-session ids are derived from (game session, user), so a
// player who drops and rejoins a room gets the same name back. Per token, the
// old stream closing late ran its deferred deletes against the documents the
// rejoined console had just written, and a refresh started with no
// deferments at all. Here the cleanup runs only when the last stream for the
// name closes, with whatever the latest registration was.
type userSessionCleanup struct {
	streams    int
	deferments map[string][]*syncpb.WriteOperation
}

func (s *Server) cleanupFor(userSession string) *userSessionCleanup {
	c := s.cleanups[userSession]
	if c == nil {
		c = &userSessionCleanup{deferments: map[string][]*syncpb.WriteOperation{}}
		s.cleanups[userSession] = c
	}
	return c
}

func (s *Server) stageDeferments(caller *session, ops []*syncpb.DefermentOperation) {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	c := s.cleanupFor(caller.userSession)
	for _, op := range ops {
		if update := op.GetUpdateDeferment(); update != nil {
			d := update.GetDeferment()
			writes := make([]*syncpb.WriteOperation, 0, len(d.GetWriteOperations()))
			for _, write := range d.GetWriteOperations() {
				writes = append(writes, proto.Clone(write).(*syncpb.WriteOperation))
			}
			c.deferments[d.GetName()] = writes
		} else if del := op.GetDeleteDeferment(); del != nil {
			delete(c.deferments, del.GetName())
		}
	}
	// Log names, not fields or values: the former identify the cleanup being
	// registered without recording a player's room state.
	s.log.Info("gamesync: deferments staged", "user", caller.userID, "count", len(c.deferments))
}

func (s *Server) beginSessionStream(caller *session) {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	s.cleanupFor(caller.userSession).streams++
}

func (s *Server) endSessionStream(caller *session) {
	s.cleanupMu.Lock()
	c := s.cleanups[caller.userSession]
	if c == nil {
		s.cleanupMu.Unlock()
		return
	}
	c.streams--
	if c.streams > 0 {
		s.cleanupMu.Unlock()
		return
	}
	deferred := c.deferments
	delete(s.cleanups, caller.userSession)
	s.cleanupMu.Unlock()

	names := make([]string, 0, len(deferred))
	for name := range deferred {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		_, commit, err := s.docsFor(caller).apply(deferred[name])
		if err != nil {
			s.log.Warn("gamesync: deferred cleanup failed", "name", name, "error", err)
			continue
		}
		s.watch.publish(scopeOf(caller), commit.changes)
	}
}
