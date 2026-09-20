package npln

import (
	"context"
	"errors"

	"github.com/n-popescu/switchnet/internal/db"
	authpb "github.com/n-popescu/switchnet/proto/auth/v1"
)

// ---- nn.npln.auth.v1.UserService -------------------------------------------
//
// A 2026-09-20 decompile of Splatoon 3 v11.3.0's own main confirms this
// service is real and linked (the literal string "nn.npln.auth.v1.UserService"
// appears in its .rodata), and that this server was answering nothing for it
// at all before now -- not Unimplemented-with-a-log-line, the silent kind:
// an RPC on an unregistered service, which register()'s own doc comment
// already names as the worse of the two failure modes.

type userService struct {
	authpb.UnimplementedUserServiceServer
	s *Server
}

// GetUser resolves a user resource to what this server can honestly say
// about it.
//
// Only Name is answered. auth.v1.User also carries Account, CreateTime,
// LastLoginTime and ShortId, but SwitchNet models none of the four: there is
// no cross-tenant "account" resource here (every user is already scoped to
// this server's one tenant), and NPLN users are created implicitly on first
// IssueToken rather than through a tracked CreateUser call, so there is no
// honest creation or last-login instant to report and no assigned short id.
// Fabricating any of the four would be inventing account state rather than
// reporting it -- the same reasoning IssueToken's own doc comment gives for
// leaving RefreshToken unanswered.
//
// TODO(capture-needed): whether a real client treats a User response with
// these four fields absent as different from one where they carry real
// values -- if some field is load-bearing, this needs a store column to
// answer, not a bigger struct literal.
func (u *userService) GetUser(
	ctx context.Context, req *authpb.GetUserRequest,
) (*authpb.User, error) {
	if _, ok := CallerFrom(ctx); !ok {
		return nil, ErrUnauthenticated
	}
	id := lastSegment(req.GetName())
	if _, err := u.s.store.GetUser(ctx, id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, ErrNotFound
		}
		u.s.log.Error("npln: load user", "user", id, "error", err)
		return nil, errInternal()
	}
	return &authpb.User{Name: userResourceName(id)}, nil
}

// QueryUserExternalIds and QueryUserShortIds are left to the embedded
// Unimplemented default.
//
// Both map an NPLN user onto a different id space -- the external identity
// provider's own id, or a compact numeric "short id" -- that only matters if
// something on this server or another NPLN service needs to look a user up
// by one. Nothing here does: SwitchNet's own code always addresses a user by
// its resource name. Answering either convincingly would mean inventing an
// id scheme with no consumer to check it against.
//
// TODO(capture-needed): whether Splatoon 3 calls either, and for what.
