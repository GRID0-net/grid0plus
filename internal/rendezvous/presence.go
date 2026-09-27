package rendezvous

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/n-popescu/grid0plus/internal/nex"
)

// A console playing online over NEX is online: the same liveness signal
// every authenticated BAAS request and every NPLN call records
// (internal/baas/presence.go's "seen recently" branch). Without it, a
// console that touches BAAS only at login and then spends the evening in
// Splatoon 2's own traffic would drop off its friends' lists as offline.
//
// What a friend sees beyond "online" -- "playing Splatoon 2" -- is the
// console's own presence report to BAAS, which it makes itself; nothing
// here second-guesses it.

// touchEvery bounds the writes: a console makes a call every few seconds
// in a match, and liveness is measured in minutes.
const touchEvery = 30 * time.Second

type touchKey struct{}

func (s *Server) registerPresence() {
	s.OnConnect(func(c *nex.Conn) { s.touch(c) })
	s.OnCall(func(c *nex.Conn, _ *nex.Request, _ *nex.Response) { s.touch(c) })
}

func (s *Server) touch(c *nex.Conn) {
	if s.store == nil || c.PID == 0 {
		return
	}
	last, _ := c.Value(touchKey{}).(*atomic.Int64)
	if last == nil {
		last = &atomic.Int64{}
		c.SetValue(touchKey{}, last)
	}
	now := s.now()
	prev := last.Load()
	if prev != 0 && now.Sub(time.Unix(0, prev)) < touchEvery {
		return
	}
	if !last.CompareAndSwap(prev, now.UnixNano()) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.TouchPresence(ctx, UserIDForPID(c.PID), now); err != nil {
		// Liveness is a nicety; a failed write must not fail the call.
		s.log.Warn("nex: touch presence", "pid", c.PID, "error", err)
	}
}
