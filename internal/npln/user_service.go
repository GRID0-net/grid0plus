package npln

import (
	"context"
	"errors"

	"github.com/n-popescu/grid0plus/internal/db"
	authpb "github.com/n-popescu/grid0plus/proto/auth/v1"
)

// ---- nn.npln.auth.v1.UserService -------------------------------------------
//
// This service is real and linked (the literal string
// "nn.npln.auth.v1.UserService" appears in Splatoon 3's binary). An RPC on an
// unregistered service fails silently, which register()'s own doc comment names
// as the worse of the two failure modes (the other being Unimplemented with a
// log line).

type userService struct {
	authpb.UnimplementedUserServiceServer
	s *Server
}

// GetUser resolves a user resource to what this server can honestly say
// about it.
//
// Only Name is answered. auth.v1.User also carries Account, CreateTime,
// LastLoginTime and ShortId, but GRID0+ models none of the four: there is
// no cross-tenant "account" resource here (every user is already scoped to
// this server's one tenant), and NPLN users are created implicitly on first
// IssueToken rather than through a tracked CreateUser call, so there is no
// honest creation or last-login instant to report and no assigned short id.
// Fabricating any of the four would be inventing account state rather than
// reporting it.
//
// UNCONFIRMED: whether a real client treats a User response with
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
		if !callerGone(err) {
			if !callerGone(err) {
				u.s.log.Error("npln: load user", "user", id, "error", err)
			}
		}
		return nil, errInternal()
	}
	return &authpb.User{Name: u.s.userResourceName(id)}, nil
}

// QueryUserExternalIds maps NPLN users to their Nintendo Service Account
// ids and back. Splatoon 3 links it (the method name is in its descriptor
// pool); the NSA id is what a Switch friend request is addressed by, which
// is the one thing the game can want another player's for.
//
// On GRID0+ the two ids are the same: an NPLN user id is the BAAS user id
// the id token names (exchangeIDToken), and that is the NSA id. So both
// directions answer the id itself -- but only for an account that exists
// and is not deleted, so the call cannot be used to probe or invent ids.
//
// UNCONFIRMED: UserExternalId.name's format. This answers
// "<user>/externalIds/<type>"; no capture shows Nintendo's. Change: this
// function.
func (u *userService) QueryUserExternalIds(
	ctx context.Context, req *authpb.QueryUserExternalIdsRequest,
) (*authpb.QueryUserExternalIdsResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}
	var ids []string
	if c := req.GetExternalIdCondition(); c != nil {
		for _, t := range c.GetExternalIdTypes() {
			if t != authpb.ExternalIdType_NSA_ID {
				return &authpb.QueryUserExternalIdsResponse{}, nil
			}
		}
		for _, name := range c.GetUsers() {
			id := lastSegment(name)
			if id == "" || id == "current" {
				id = caller.UserID
			}
			ids = append(ids, id)
		}
	} else {
		for _, e := range req.GetUserCondition().GetExternalIds() {
			if e.GetType() == authpb.ExternalIdType_NSA_ID {
				ids = append(ids, e.GetId())
			}
		}
	}
	if len(ids) > 100 {
		ids = ids[:100]
	}

	out := &authpb.QueryUserExternalIdsResponse{}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		user, err := u.s.store.GetUser(ctx, id)
		if errors.Is(err, db.ErrNotFound) || (err == nil && user.Deleted) {
			continue
		}
		if err != nil {
			if !callerGone(err) {
				if !callerGone(err) {
					u.s.log.Error("npln: load user", "user", id, "error", err)
				}
			}
			return nil, errInternal()
		}
		name := u.s.userResourceName(id)
		out.UserExternalIds = append(out.UserExternalIds, &authpb.UserExternalId{
			Name:       name + "/externalIds/" + authpb.ExternalIdType_NSA_ID.String(),
			User:       name,
			ExternalId: &authpb.ExternalId{Type: authpb.ExternalIdType_NSA_ID, Id: id},
		})
	}
	u.s.log.Info("npln: external ids queried", "user", caller.UserID,
		"asked", len(ids), "answered", len(out.UserExternalIds))
	return out, nil
}

// QueryUserShortIds is left to the embedded Unimplemented default: GRID0+
// assigns no numeric short ids, and the game's descriptor pool does not
// name the method.
