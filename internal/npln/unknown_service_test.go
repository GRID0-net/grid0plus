package npln

import (
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	ugcpb "github.com/n-popescu/switchnet/proto/ugcstore/v1"
)

// TestAnUnregisteredServiceAnswersInTwoFrames pins the answer to a service this
// server does not register (Ugcstore here). grpc-go's own default is a single
// Trailers-Only frame -- no response headers at all -- which is the shape
// measured crashing Splatoon 3 (2162-0001). Response headers arriving, with
// npln-grpc-type in them, is what proves the reply was split in two.
func TestAnUnregisteredServiceAnswersInTwoFrames(t *testing.T) {
	h := newHarness(t)
	h.createUser("ugc-user", "device-ugc")
	ctx := h.ctx(h.sessionFor(t, "ugc-user", "device-ugc"), "")

	var header metadata.MD
	_, err := ugcpb.NewUgcstoreClient(h.conn).GetDocument(ctx,
		&ugcpb.GetDocumentRequest{Name: "tenants/" + testTenant + "/documents/x"},
		grpc.Header(&header))
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("err = %v, want Unimplemented", err)
	}
	if got := header.Get("npln-grpc-type"); len(got) == 0 {
		t.Errorf("no response headers (%v): the reply was a single Trailers-Only frame", header)
	}

	// And it is still behind authentication, like every registered service.
	_, err = ugcpb.NewUgcstoreClient(h.conn).GetDocument(h.ctx("", ""),
		&ugcpb.GetDocumentRequest{Name: "tenants/" + testTenant + "/documents/x"})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("unauthenticated call: err = %v, want Unauthenticated", err)
	}
}
