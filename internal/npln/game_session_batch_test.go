package npln

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mmpb "github.com/n-popescu/grid0plus/proto/matchmaking/v1"
)

// matchTwoPlayers queues both, returning the formed session's name and each
// player's own authenticated context. Shared setup for
// BatchGetGameSessions/ListUserSessions/GetUserSession tests, all of which
// need a real match to exist first.
func matchTwoPlayers(t *testing.T, h *harness) (name string, one, two context.Context) {
	t.Helper()
	h.createUser("player-one0000000", "device-one")
	h.createUser("player-two0000000", "device-two")

	one = h.ctx(h.sessionFor(t, "player-one0000000", "device-one"), "")
	two = h.ctx(h.sessionFor(t, "player-two0000000", "device-two"), "")

	client := mmpb.NewMatchmakerClient(h.conn)
	if _, err := client.CreateMatchmakingTicket(one, ticketFor("turf-war")); err != nil {
		t.Fatalf("CreateMatchmakingTicket (one): %v", err)
	}
	matched, err := client.CreateMatchmakingTicket(two, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("CreateMatchmakingTicket (two): %v", err)
	}
	matched = trackResult(t, client, two, matched)
	name = matched.GetGameSession().GetName()
	if name == "" {
		t.Fatal("no session was formed")
	}
	return name, one, two
}

func TestBatchGetGameSessionsReturnsOnlyWhatTheCallerCanSee(t *testing.T) {
	h := newHarness(t)
	h.createUser("outsider0000000000", "device-outsider")
	outsider := h.ctx(h.sessionFor(t, "outsider0000000000", "device-outsider"), "")

	name, one, _ := matchTwoPlayers(t, h)

	sessions := mmpb.NewGameSessionServiceClient(h.conn)

	// The caller's real session, plus a name that does not exist, plus one
	// the caller was never matched into: only the first should come back.
	got, err := sessions.BatchGetGameSessions(one, &mmpb.BatchGetGameSessionsRequest{
		Names: []string{name, "tenants/dce9377b/gameSessions/does-not-exist"},
	})
	if err != nil {
		t.Fatalf("BatchGetGameSessions: %v", err)
	}
	if len(got.GetGameSessions()) != 1 || got.GetGameSessions()[0].GetName() != name {
		t.Fatalf("BatchGetGameSessions = %+v, want just %q", got.GetGameSessions(), name)
	}

	// An outsider batch-getting the same real name sees nothing, the same
	// silent omission GetGameSession itself already gives a stranger.
	gotOutsider, err := sessions.BatchGetGameSessions(outsider, &mmpb.BatchGetGameSessionsRequest{
		Names: []string{name},
	})
	if err != nil {
		t.Fatalf("BatchGetGameSessions (outsider): %v", err)
	}
	if len(gotOutsider.GetGameSessions()) != 0 {
		t.Errorf("outsider's batch = %+v, want empty", gotOutsider.GetGameSessions())
	}
}

func TestListUserSessionsMatchesGetGameSessionsEmbeddedList(t *testing.T) {
	h := newHarness(t)
	name, one, _ := matchTwoPlayers(t, h)

	sessions := mmpb.NewGameSessionServiceClient(h.conn)
	fromGet, err := sessions.GetGameSession(one, &mmpb.GetGameSessionRequest{Name: name})
	if err != nil {
		t.Fatalf("GetGameSession: %v", err)
	}
	fromList, err := sessions.ListUserSessions(one, &mmpb.ListUserSessionsRequest{Parent: name})
	if err != nil {
		t.Fatalf("ListUserSessions: %v", err)
	}
	if len(fromList.GetUserSessions()) != len(fromGet.GetUserSessions()) {
		t.Fatalf("ListUserSessions returned %d, GetGameSession embedded %d",
			len(fromList.GetUserSessions()), len(fromGet.GetUserSessions()))
	}
	for i, us := range fromList.GetUserSessions() {
		if us.GetName() != fromGet.GetUserSessions()[i].GetName() {
			t.Errorf("entry %d: ListUserSessions %q != GetGameSession %q",
				i, us.GetName(), fromGet.GetUserSessions()[i].GetName())
		}
	}
}

func TestListUserSessionsRefusesAStranger(t *testing.T) {
	h := newHarness(t)
	h.createUser("outsider0000000000", "device-outsider")
	outsider := h.ctx(h.sessionFor(t, "outsider0000000000", "device-outsider"), "")
	name, _, _ := matchTwoPlayers(t, h)

	sessions := mmpb.NewGameSessionServiceClient(h.conn)
	if _, err := sessions.ListUserSessions(outsider, &mmpb.ListUserSessionsRequest{Parent: name}); status.Code(err) != codes.NotFound {
		t.Errorf("ListUserSessions by an outsider: got %v, want NotFound", err)
	}
}

func TestGetUserSessionResolvesOneMatchedPlayer(t *testing.T) {
	h := newHarness(t)
	name, one, _ := matchTwoPlayers(t, h)

	sessions := mmpb.NewGameSessionServiceClient(h.conn)
	list, err := sessions.ListUserSessions(one, &mmpb.ListUserSessionsRequest{Parent: name})
	if err != nil {
		t.Fatalf("ListUserSessions: %v", err)
	}
	if len(list.GetUserSessions()) == 0 {
		t.Fatal("no user sessions to look one up from")
	}
	want := list.GetUserSessions()[0]

	got, err := sessions.GetUserSession(one, &mmpb.GetUserSessionRequest{Name: want.GetName()})
	if err != nil {
		t.Fatalf("GetUserSession: %v", err)
	}
	if got.GetName() != want.GetName() || got.GetUser() != want.GetUser() {
		t.Errorf("GetUserSession = %+v, want %+v", got, want)
	}
}

func TestGetUserSessionRefusesAStranger(t *testing.T) {
	h := newHarness(t)
	h.createUser("outsider0000000000", "device-outsider")
	outsider := h.ctx(h.sessionFor(t, "outsider0000000000", "device-outsider"), "")
	name, one, _ := matchTwoPlayers(t, h)

	sessions := mmpb.NewGameSessionServiceClient(h.conn)
	list, err := sessions.ListUserSessions(one, &mmpb.ListUserSessionsRequest{Parent: name})
	if err != nil {
		t.Fatalf("ListUserSessions: %v", err)
	}
	target := list.GetUserSessions()[0].GetName()

	if _, err := sessions.GetUserSession(outsider, &mmpb.GetUserSessionRequest{Name: target}); status.Code(err) != codes.NotFound {
		t.Errorf("GetUserSession by an outsider: got %v, want NotFound", err)
	}
}

func TestGetUserSessionRefusesAMissingUserWithinARealSession(t *testing.T) {
	h := newHarness(t)
	name, one, _ := matchTwoPlayers(t, h)

	sessions := mmpb.NewGameSessionServiceClient(h.conn)
	missing := name + "/userSessions/nobody-was-ever-this-player"
	if _, err := sessions.GetUserSession(one, &mmpb.GetUserSessionRequest{Name: missing}); status.Code(err) != codes.NotFound {
		t.Errorf("GetUserSession for a nonexistent player: got %v, want NotFound", err)
	}
}
