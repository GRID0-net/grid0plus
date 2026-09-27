package npln

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// TestPublishingADrawingOrALockerIsAcknowledged: RegisterDocument is the
// last step of posting either, and it answered Unimplemented -- a
// communication error at the end of posting.
func TestPublishingADrawingOrALockerIsAcknowledged(t *testing.T) {
	h := newHarness(t)
	h.createUser("artist0000000001", "device-artist-1")
	ctx := h.ctx(h.sessionFor(t, "artist0000000001", "device-artist-1"), "")

	if _, err := toyohrpb.NewCanolaClient(h.conn).RegisterDocument(ctx, &toyohrpb.CanolaRegisterDocumentRequest{
		Name: "tenants/current/documents/services/toyohr/interfaces/canola/posts/x",
	}); err != nil {
		t.Errorf("Canola.RegisterDocument: %v", err)
	}
	if _, err := toyohrpb.NewLockerClient(h.conn).RegisterDocument(ctx, &toyohrpb.LockerRegisterDocumentRequest{
		Name: "tenants/current/documents/services/toyohr/interfaces/locker/lockerPosts/x",
	}); err != nil {
		t.Errorf("Locker.RegisterDocument: %v", err)
	}

	if _, err := toyohrpb.NewLockerClient(h.conn).RegisterDocument(h.ctx("", ""), &toyohrpb.LockerRegisterDocumentRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("unauthenticated RegisterDocument: %v, want Unauthenticated", err)
	}
}
