package npln

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/presence"
	"github.com/n-popescu/switchnet/internal/presencebus"
	friendspb "github.com/n-popescu/switchnet/proto/friends/v1"
)

// One friend graph, not two: this file reads internal/db's friendship store
// directly, the same rows internal/baas/friends.go reads. See
// docs/ROADMAP.md#2.5.

// defaultFriendsPageSize and maxFriendsPageSize mirror internal/baas's own
// defaultFriendCount and maxFriendCount, for the same reasons -- a console
// that does not ask for a size gets a generous default, and one that asks for
// an unreasonable size is capped rather than handed the whole table.
const (
	defaultFriendsPageSize = 300
	maxFriendsPageSize     = 1000
)

// friendsKeepAlive is how often SubscribeFriendUsers resends its snapshot
// when nothing has changed.
//
// SubscribeFriendUsersResponse carries no separate heartbeat-only variant --
// every message on the stream is a full friend_accounts/blocking_accounts
// snapshot, unlike PresenceService's own stream. So "keeping it warm" here
// means periodically resending everything, not a lightweight ping. ROADMAP
// §2.5: "a stream that goes quiet is treated as dead and the friend list
// stops updating with nothing in the log."
const friendsKeepAlive = 30 * time.Second

// ---- nn.npln.friends.v1.Friends -------------------------------------------

type friendsService struct {
	friendspb.UnimplementedFriendsServer
	s *Server
}

// ActivateUser is called before the other Friends RPCs, per the reference
// implementation's boot order (internal/npln/README.md).
//
// TODO(capture-needed): what "activating" a user actually changes server-side.
// SwitchNet has no separate activation state to flip -- presence is already
// resolved fresh on every read (internal/presence), and there is nothing else
// here that is "off" until this is called. The defensible reading is that
// this exists for titles or platforms where friends data is not always
// wanted, and acknowledging it is enough; if a capture shows the console
// waiting on some effect this does not produce, change: this function.
func (f *friendsService) ActivateUser(
	ctx context.Context, req *friendspb.ActivateUserRequest,
) (*friendspb.ActivateUserResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}
	if id := lastSegment(req.GetName()); id != "" && id != caller.UserID {
		// Same rule as everywhere else a request names a user: the token
		// decides who this call is about, not a field the client filled in.
		f.s.log.Warn("npln: ActivateUser for a user other than the caller",
			"caller", caller.UserID, "name", req.GetName())
		return nil, ErrPermissionDenied
	}
	return &friendspb.ActivateUserResponse{}, nil
}

// ListFriendUsers returns the caller's friends, each with the caller's own
// relationship to them.
func (f *friendsService) ListFriendUsers(
	ctx context.Context, req *friendspb.ListFriendUsersRequest,
) (*friendspb.ListFriendUsersResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	// req.Parent names a user, but it is the client's own claim about who it
	// is -- the same reasoning AllocateIceServerSet's doc comment gives for
	// ignoring the request's user field entirely and serving the caller's
	// own data. Pagination beyond one page is not implemented, matching
	// internal/baas's own friend list, which has the same limit.
	count := defaultFriendsPageSize
	if n := req.GetPageSize(); n > 0 {
		count = min(int(n), maxFriendsPageSize)
	}

	callerUser, err := f.s.store.GetUser(ctx, caller.UserID)
	if err != nil {
		f.s.log.Error("npln: load caller", "user", caller.UserID, "error", err)
		return nil, errInternal()
	}

	friendships, err := f.s.store.ListFriends(ctx, caller.UserID, count)
	if err != nil {
		f.s.log.Error("npln: list friends", "user", caller.UserID, "error", err)
		return nil, errInternal()
	}

	out := make([]*friendspb.FriendUser, 0, len(friendships))
	for _, fr := range friendships {
		friend, err := f.s.store.GetUser(ctx, fr.FriendID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				f.s.log.Warn("npln: friendship names a missing user",
					"user", caller.UserID, "friend", fr.FriendID)
				continue
			}
			f.s.log.Error("npln: load friend", "friend", fr.FriendID, "error", err)
			return nil, errInternal()
		}
		out = append(out, f.s.friendUserProto(ctx, callerUser, friend, fr))
	}

	return &friendspb.ListFriendUsersResponse{FriendUsers: out}, nil
}

// ListBlockingUsers returns who the caller has blocked.
func (f *friendsService) ListBlockingUsers(
	ctx context.Context, req *friendspb.ListBlockingUsersRequest,
) (*friendspb.ListBlockingUsersResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}

	blocks, err := f.s.store.ListBlocks(ctx, caller.UserID)
	if err != nil {
		f.s.log.Error("npln: list blocks", "user", caller.UserID, "error", err)
		return nil, errInternal()
	}

	out := make([]*friendspb.BlockingUser, 0, len(blocks))
	for _, b := range blocks {
		blocked, err := f.s.store.GetUser(ctx, b.BlockedUserID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				continue
			}
			f.s.log.Error("npln: load blocked user", "blocked", b.BlockedUserID, "error", err)
			return nil, errInternal()
		}
		out = append(out, f.s.blockingUserProto(caller.UserID, blocked))
	}

	return &friendspb.ListBlockingUsersResponse{BlockingUsers: out}, nil
}

// SubscribeFriendUsers streams the caller's friends and blocks, resending the
// full snapshot on a timer and whenever a friend's presence changes.
func (f *friendsService) SubscribeFriendUsers(
	req *friendspb.SubscribeFriendUsersRequest,
	stream grpc.ServerStreamingServer[friendspb.SubscribeFriendUsersResponse],
) error {
	ctx := stream.Context()
	caller, ok := CallerFrom(ctx)
	if !ok {
		return ErrUnauthenticated
	}

	snapshot, friendIDs, err := f.s.friendsSnapshot(ctx, caller.UserID)
	if err != nil {
		f.s.log.Error("npln: build friends snapshot", "user", caller.UserID, "error", err)
		return errInternal()
	}
	if err := stream.Send(snapshot); err != nil {
		return err
	}

	var events <-chan presencebus.Event
	if f.s.bus != nil {
		var err error
		events, err = f.s.bus.Subscribe(ctx)
		if err != nil {
			f.s.log.Error("npln: subscribe presence bus", "error", err)
			// Not fatal to the stream: it still works on its own keep-alive
			// timer, the same degradation as no bus configured at all.
			events = nil
		}
	}

	ticker := time.NewTicker(friendsKeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			snapshot, friendIDs, err = f.s.friendsSnapshot(ctx, caller.UserID)
			if err != nil {
				f.s.log.Error("npln: build friends snapshot", "user", caller.UserID, "error", err)
				return errInternal()
			}
			if err := stream.Send(snapshot); err != nil {
				return err
			}
		case e, open := <-events:
			if !open {
				events = nil
				continue
			}
			if !friendIDs[e.UserID] {
				continue
			}
			snapshot, friendIDs, err = f.s.friendsSnapshot(ctx, caller.UserID)
			if err != nil {
				f.s.log.Error("npln: build friends snapshot", "user", caller.UserID, "error", err)
				return errInternal()
			}
			if err := stream.Send(snapshot); err != nil {
				return err
			}
		}
	}
}

// friendsSnapshot builds one SubscribeFriendUsersResponse and the set of
// friend ids it carries, so the caller can tell whether a later presence
// event is worth resending for.
func (s *Server) friendsSnapshot(ctx context.Context, userID string) (*friendspb.SubscribeFriendUsersResponse, map[string]bool, error) {
	callerUser, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	friendships, err := s.store.ListFriends(ctx, userID, maxFriendsPageSize)
	if err != nil {
		return nil, nil, err
	}
	blocks, err := s.store.ListBlocks(ctx, userID)
	if err != nil {
		return nil, nil, err
	}

	friendIDs := make(map[string]bool, len(friendships))
	friendAccounts := make([]*friendspb.SubscribeFriendUsersResponse_FriendAccount, 0, len(friendships))
	for _, fr := range friendships {
		friend, err := s.store.GetUser(ctx, fr.FriendID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				continue
			}
			return nil, nil, err
		}
		friendIDs[fr.FriendID] = true
		// One user per NSA account: SwitchNet has no notion of more than one
		// friends-visible profile behind a single account, unlike the
		// collection this message's shape allows for.
		friendAccounts = append(friendAccounts, &friendspb.SubscribeFriendUsersResponse_FriendAccount{
			NsaId: fr.FriendID,
			Users: []*friendspb.FriendUser{s.friendUserProto(ctx, callerUser, friend, fr)},
		})
	}

	blockingAccounts := make([]*friendspb.SubscribeFriendUsersResponse_BlockingAccount, 0, len(blocks))
	for _, b := range blocks {
		blocked, err := s.store.GetUser(ctx, b.BlockedUserID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				continue
			}
			return nil, nil, err
		}
		blockingAccounts = append(blockingAccounts, &friendspb.SubscribeFriendUsersResponse_BlockingAccount{
			NsaId: b.BlockedUserID,
			Users: []*friendspb.BlockingUser{s.blockingUserProto(userID, blocked)},
		})
	}

	return &friendspb.SubscribeFriendUsersResponse{
		FriendAccounts:    friendAccounts,
		BlockingAccounts:  blockingAccounts,
		KeepAliveInterval: durationpb.New(friendsKeepAlive),
	}, friendIDs, nil
}

// friendUserProto renders one friend as the wire message, from the caller's
// point of view.
//
// TODO(capture-needed): the resource-name shape (Name below), same as
// matchmaking.go's ticketProto -- lastSegment parses whatever comes back
// leniently, so a wrong prefix costs a cosmetic mismatch rather than a
// broken call.
func (s *Server) friendUserProto(ctx context.Context, callerUser, friend db.User, fr db.Friendship) *friendspb.FriendUser {
	return &friendspb.FriendUser{
		Name:       s.tenantPath() + "/users/" + callerUser.ID + "/friendUsers/" + friend.ID,
		FriendUser: userResourceName(friend.ID),
		NsaId:      friend.ID,
		Relationship: &friendspb.FriendUser_Relationship{
			Favorite: fr.Favorite,
			// PresenceReceivable: may the caller see the FRIEND's presence,
			// per the friend's own privacy setting.
			PresenceReceivable: presence.Visible(ctx, s.store, friend, callerUser.ID),
			// PresenceDeliverable: may the FRIEND see the caller's presence,
			// per the caller's own privacy setting. Both must be set, or the
			// client neither shows a friend's presence nor publishes its own --
			// see docs/ROADMAP.md#2.5.
			PresenceDeliverable: presence.Visible(ctx, s.store, callerUser, friend.ID),
		},
	}
}

// blockingUserProto renders one block as the wire message.
func (s *Server) blockingUserProto(callerID string, blocked db.User) *friendspb.BlockingUser {
	return &friendspb.BlockingUser{
		Name:         s.tenantPath() + "/users/" + callerID + "/blockingUsers/" + blocked.ID,
		BlockingUser: userResourceName(blocked.ID),
		NsaId:        blocked.ID,
	}
}
