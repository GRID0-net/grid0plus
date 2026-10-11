package service

import (
	"context"
	"net"
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
