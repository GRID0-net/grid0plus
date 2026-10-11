package service

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// TestAHeldStreamDoesNotStallShutdown: a client holding a server stream open
// -- what every online console does -- must not keep the server from
// stopping past the grace period.
func TestAHeldStreamDoesNotStallShutdown(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stream, err := healthpb.NewHealthClient(conn).Watch(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	StopGRPC(srv, 200*time.Millisecond)
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("stop took %v with a held stream, want about the 200ms grace", took)
	}
}

// TestAHeldHTTPRequestDoesNotStallShutdown: a request held open until its
// context ends -- a console's frontline, a dashboard live view -- must be
// cancelled by shutdown, not sit out the grace and fail the stop.
func TestAHeldHTTPRequestDoesNotStallShutdown(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	lis.Close()

	started := make(chan struct{})
	srv := &http.Server{Addr: addr, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), srv) }()

	var resp *http.Response
	for i := 0; i < 50; i++ {
		if resp, err = http.Get("http://" + addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-started

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve: %v, want a clean stop", err)
		}
		if took := time.Since(start); took > heldRequestGrace+time.Second {
			t.Errorf("stop took %v, want about heldRequestGrace", took)
		}
	case <-time.After(shutdownGrace + 2*time.Second):
		t.Fatal("Serve did not return")
	}
}
