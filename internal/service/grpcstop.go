package service

import (
	"time"

	"google.golang.org/grpc"
)

// StopGRPC stops srv gracefully, but for at most grace.
//
// GracefulStop waits for every RPC to finish, and a console holds its server
// streams (presence, friends, gamesync sessions) open for as long as it is
// online. Unbounded, a restart waited until systemd killed the process,
// ~70 s in which nothing answered. In-flight unary calls still get grace to
// finish; the held streams are then closed, and the console reconnects.
func StopGRPC(srv *grpc.Server, grace time.Duration) {
	done := make(chan struct{})
	go func() {
		srv.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		srv.Stop()
		<-done
	}
}
