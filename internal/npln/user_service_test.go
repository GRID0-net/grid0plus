package npln

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authpb "github.com/n-popescu/grid0plus/proto/auth/v1"
)

func TestGetUserResolvesAnExistingUserByName(t *testing.T) {
	h := newHarness(t)
	h.createUser("someone", "device-someone")
	bearer := h.sessionFor(t, "someone", "device-someone")
	ctx := h.ctx(bearer, "")

	client := authpb.NewUserServiceClient(h.conn)
	got, err := client.GetUser(ctx, &authpb.GetUserRequest{Name: h.srv.userResourceName("someone")})
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.GetName() != h.srv.userResourceName("someone") {
		t.Errorf("Name = %q, want %q", got.GetName(), h.srv.userResourceName("someone"))
	}
}

func TestGetUserRefusesAMissingUser(t *testing.T) {
	h := newHarness(t)
	h.createUser("someone", "device-someone")
	bearer := h.sessionFor(t, "someone", "device-someone")
	ctx := h.ctx(bearer, "")

	client := authpb.NewUserServiceClient(h.conn)
	_, err := client.GetUser(ctx, &authpb.GetUserRequest{Name: h.srv.userResourceName("nobody")})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err = %v, want NotFound", err)
	}
}

func TestGetUserRefusesAnUnauthenticatedCaller(t *testing.T) {
	h := newHarness(t)
	h.createUser("someone", "device-someone")

	client := authpb.NewUserServiceClient(h.conn)
	_, err := client.GetUser(h.ctx("", ""), &authpb.GetUserRequest{Name: h.srv.userResourceName("someone")})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("err = %v, want Unauthenticated", err)
	}
}
