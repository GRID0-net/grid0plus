package npln

import (
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	timberpb "github.com/n-popescu/grid0plus/proto/timber/v1"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// TestAnUnregisteredServiceAnswersInTwoFrames pins the answer to a service this
// server does not register (Timber's TradeBoxService here). grpc-go's own default is a single
// Trailers-Only frame -- no response headers at all -- which is the shape
// measured crashing Splatoon 3 (2162-0001). Response headers arriving, with
// npln-grpc-type in them, is what proves the reply was split in two.
func TestAnUnregisteredServiceAnswersInTwoFrames(t *testing.T) {
	h := newHarness(t)
	h.createUser("ugc-user", "device-ugc")
	ctx := h.ctx(h.sessionFor(t, "ugc-user", "device-ugc"), "")

	var header metadata.MD
	_, err := timberpb.NewTradeBoxServiceClient(h.conn).CreateTradeBox(ctx,
		&timberpb.CreateTradeBoxRequest{}, grpc.Header(&header))
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("err = %v, want Unimplemented", err)
	}
	if got := header.Get("npln-grpc-type"); len(got) == 0 {
		t.Errorf("no response headers (%v): the reply was a single Trailers-Only frame", header)
	}

	// And it is still behind authentication, like every registered service.
	_, err = timberpb.NewTradeBoxServiceClient(h.conn).CreateTradeBox(h.ctx("", ""), &timberpb.CreateTradeBoxRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("unauthenticated call: err = %v, want Unauthenticated", err)
	}
}

// A missing violation must have its own response headers. A named violation
// declares the player sanctioned; a Trailers-Only error can crash this client.
func TestNoViolationIsATwoFrameNotFound(t *testing.T) {
	h := newHarness(t)
	h.createUser("screening-user", "device-screening")
	ctx := h.ctx(h.sessionFor(t, "screening-user", "device-screening"), "")
	client := toyohrpb.NewUserScreeningClient(h.conn)

	var header metadata.MD
	violation, err := client.GetViolation(ctx,
		&toyohrpb.GetViolationRequest{Name: "tenants/current/users/current/violations/inappropriate_nickname"},
		grpc.Header(&header))
	if violation != nil || status.Code(err) != codes.NotFound {
		t.Fatalf("GetViolation = %v, %v; want no violation and NotFound", violation, err)
	}
	if got := header.Get("npln-grpc-type"); len(got) != 1 || got[0] != "Unary" {
		t.Errorf("response headers = %v; want Unary header before status trailers", header)
	}
	// Never content-length: 0 -- behind nginx's grpc_pass it drops the
	// trailers, and the console reads the call as UNKNOWN (2026-09-25 hardware).
	if got := header.Get("content-length"); len(got) != 0 {
		t.Errorf("response headers = %v; content-length must be absent", header)
	}

	_, err = client.GetViolation(h.ctx("", ""), &toyohrpb.GetViolationRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("unauthenticated GetViolation = %v; want Unauthenticated", err)
	}
}
