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

func (s *Server) stageDeferments(caller *session, ops []*syncpb.DefermentOperation) {
	caller.mu.Lock()
	defer caller.mu.Unlock()
	if caller.deferments == nil {
		caller.deferments = make(map[string][]*syncpb.WriteOperation)
	}
	for _, op := range ops {
		if update := op.GetUpdateDeferment(); update != nil {
			d := update.GetDeferment()
			writes := make([]*syncpb.WriteOperation, 0, len(d.GetWriteOperations()))
			for _, write := range d.GetWriteOperations() {
				writes = append(writes, proto.Clone(write).(*syncpb.WriteOperation))
			}
			caller.deferments[d.GetName()] = writes
		} else if del := op.GetDeleteDeferment(); del != nil {
			delete(caller.deferments, del.GetName())
		}
	}
	// Log names, not fields or values: the former identify the cleanup being
	// registered without recording a player's room state.
	s.log.Info("gamesync: deferments staged", "user", caller.userID, "count", len(caller.deferments))
}

func (s *Server) endSessionStream(caller *session) {
	caller.mu.Lock()
	caller.activeStreams--
	if caller.activeStreams > 0 {
		caller.mu.Unlock()
		return
	}
	deferred := caller.deferments
	caller.deferments = nil
	caller.mu.Unlock()

	names := make([]string, 0, len(deferred))
	for name := range deferred {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		_, commit, err := s.docs.apply(deferred[name])
		if err != nil {
			s.log.Warn("gamesync: deferred cleanup failed", "name", name, "error", err)
			continue
		}
		s.watch.publish(commit.changes)
	}
}
