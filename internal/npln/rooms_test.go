package npln

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	mmpb "github.com/n-popescu/grid0plus/proto/matchmaking/v1"
)

// openRoom runs the host's side of opening a room -- Create, then Track --
// and returns the SUCCEEDED ticket.
func openRoom(t *testing.T, h *harness, ctx context.Context, config string, props map[string]*commonpb.Value, password string) *mmpb.GameSessionCreationTicket {
	t.Helper()
	client := mmpb.NewGameSessionServiceClient(h.conn)
	created, err := client.CreateGameSessionCreationTicket(ctx, &mmpb.CreateGameSessionCreationTicketRequest{
		Parent: "tenants/current",
		GameSessionCreationTicket: &mmpb.GameSessionCreationTicket{
			MatchmakingConfig: "tenants/current/matchmakingConfigs/" + config,
			UserDefinitions:   []*mmpb.UserDefinition{{User: "tenants/current/users/current", Team: "alpha"}},
			GameSession: &mmpb.GameSession{
				Properties: &commonpb.MapValue{Fields: props},
				Password:   password,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateGameSessionCreationTicket: %v", err)
	}
	if created.GetState() != mmpb.GameSessionCreationTicket_PENDING {
		t.Fatalf("created ticket state = %v, want PENDING", created.GetState())
	}

	stream, err := client.TrackGameSessionCreationTicket(ctx, &mmpb.TrackGameSessionCreationTicketRequest{Name: created.GetName()})
	if err != nil {
		t.Fatalf("TrackGameSessionCreationTicket: %v", err)
	}
	succeeded, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv SUCCEEDED: %v", err)
	}
	// One message, then the stream ends: the close is what tells the game
	// creation is done. Held open, the game waited and cancelled.
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("after SUCCEEDED the stream should end, got %v", err)
	}
	return succeeded
}

// TestOpeningARoomNoLongerFailsWith2321_4224 is the reported bug: creating a
// private battle answered Unimplemented. Asserted on the fields a real
// server's SUCCEEDED ticket carries.
func TestOpeningARoomNoLongerFailsWith2321_4224(t *testing.T) {
	h := newHarness(t)
	h.createUser("host0000000000001", "device-host-1")
	host := h.ctx(h.sessionFor(t, "host0000000000001", "device-host-1"), "")

	ticket := openRoom(t, h, host, "private_match_config",
		map[string]*commonpb.Value{"game_mode": stringValue("turf"), "userName": stringValue("host")}, "")

	if ticket.GetState() != mmpb.GameSessionCreationTicket_SUCCEEDED {
		t.Fatalf("tracked ticket state = %v, want SUCCEEDED", ticket.GetState())
	}
	if !strings.HasPrefix(ticket.GetName(), "tenants/"+testTenant+"/gameSessionCreationTickets/") {
		t.Errorf("ticket name = %q", ticket.GetName())
	}
	if got, want := ticket.GetMatchmakingConfig(), "tenants/"+testTenant+"/matchmakingConfigs/private_match_config"; got != want {
		t.Errorf("config = %q, want the concrete tenant %q", got, want)
	}

	room := ticket.GetGameSession()
	if room.GetMaxParticipantCount() != 10 || room.GetCurrentParticipantCount() != 1 {
		t.Errorf("room is %d/%d, want 1/10: a room opens with only its host",
			room.GetCurrentParticipantCount(), room.GetMaxParticipantCount())
	}
	if !room.GetIsPublic() {
		t.Error("is_public = false; a real server marks even a private room public")
	}
	if len(room.GetUserSessions()) != 0 {
		t.Error("the ticket's game session carries user_sessions; the captured one does not")
	}
	props := room.GetProperties().GetFields()
	if props["game_mode"].GetStringValue() != "turf" || props["userName"].GetStringValue() != "host" {
		t.Errorf("the client's own properties were dropped: %v", props)
	}
	if props["_BaseConfigName"].GetStringValue() != "private_match_config" {
		t.Errorf("_BaseConfigName = %v", props["_BaseConfigName"])
	}
	if _, ok := props["_AliasSuffix"]; !ok {
		t.Error("_AliasSuffix is absent")
	}

	matched := ticket.GetMatchedUserSessions()
	if len(matched) != 1 {
		t.Fatalf("matched user sessions = %d, want the host's own", len(matched))
	}
	if got := matched[0].GetUserDefinition().GetUser(); got != "tenants/"+testTenant+"/users/host0000000000001" {
		t.Errorf("host user = %q, want the concrete name so the host can find itself", got)
	}
	if matched[0].GetUserDefinition().GetTeam() != "alpha" {
		t.Error("the host's own UserDefinition team was not kept")
	}
	payload := decodeJWTSegment(t, strings.Split(matched[0].GetMatchmakingIdToken(), ".")[1])
	gs, _ := payload["gamesync"].(map[string]any)
	if gs["gsid"] != lastSegment(room.GetName()) || payload["iss"] != "gss" {
		t.Errorf("host session token does not name the room for gamesync: %v", payload)
	}
	// Every room token in the working Nextendo capture says so.
	if gs["friend_room"] != true {
		t.Errorf("room session token friend_room = %v, want true", gs["friend_room"])
	}
}

// TestAFriendJoinsARoomByItsName is the second half: a friend who was never
// matched into the room joins it, gets a token of their own, and the count
// follows.
func TestAFriendJoinsARoomByItsName(t *testing.T) {
	h := newHarness(t)
	h.createUser("host0000000000002", "device-host-2")
	h.createUser("friend00000000002", "device-friend-2")
	host := h.ctx(h.sessionFor(t, "host0000000000002", "device-host-2"), "")
	friend := h.ctx(h.sessionFor(t, "friend00000000002", "device-friend-2"), "")
	client := mmpb.NewGameSessionServiceClient(h.conn)

	room := openRoom(t, h, host, "coop_private_config", nil, "").GetGameSession()
	if room.GetMaxParticipantCount() != 4 {
		t.Errorf("Salmon Run room capacity = %d, want the captured 4", room.GetMaxParticipantCount())
	}

	// The joiner reads the room before entering.
	seen, err := client.GetGameSession(friend, &mmpb.GetGameSessionRequest{Name: room.GetName()})
	if err != nil {
		t.Fatalf("GetGameSession by a prospective joiner: %v", err)
	}
	if len(seen.GetUserSessions()) != 1 {
		t.Errorf("GetGameSession lists %d players, want the host", len(seen.GetUserSessions()))
	}

	joined, err := client.JoinGameSession(friend, &mmpb.JoinGameSessionRequest{Name: room.GetName()})
	if err != nil {
		t.Fatalf("JoinGameSession: %v", err)
	}
	if joined.GetGameSession().GetCurrentParticipantCount() != 2 {
		t.Errorf("after one join the room holds %d", joined.GetGameSession().GetCurrentParticipantCount())
	}
	mine := joined.GetMatchedUserSessions()
	if len(mine) != 1 || mine[0].GetUserDefinition().GetUser() != "tenants/"+testTenant+"/users/friend00000000002" {
		t.Fatalf("matched user sessions = %v, want only the joiner's own", mine)
	}
	payload := decodeJWTSegment(t, strings.Split(mine[0].GetMatchmakingIdToken(), ".")[1])
	if payload["sub"] != "friend00000000002" {
		t.Errorf("joiner's token is for %v", payload["sub"])
	}

	// A retry does not count the same player twice.
	again, err := client.JoinGameSession(friend, &mmpb.JoinGameSessionRequest{Name: room.GetName()})
	if err != nil || again.GetGameSession().GetCurrentParticipantCount() != 2 {
		t.Errorf("rejoining: count %d, err %v; want 2 and no error", again.GetGameSession().GetCurrentParticipantCount(), err)
	}
}

func TestRoomSessionTokensCarrySubmittedPlayerData(t *testing.T) {
	h := newHarness(t)
	h.createUser("room-host-data", "room-host-device")
	h.createUser("room-guest-data", "room-guest-device")
	host := h.ctx(h.sessionFor(t, "room-host-data", "room-host-device"), "")
	guest := h.ctx(h.sessionFor(t, "room-guest-data", "room-guest-device"), "")
	client := mmpb.NewGameSessionServiceClient(h.conn)
	definition := func(label string) *mmpb.UserDefinition {
		return &mmpb.UserDefinition{
			User: "tenants/current/users/current",
			Team: label,
			Attributes: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"match_hash": stringValue(label),
			}},
			LatencyData: &mmpb.LatencyData{Latencies: map[string]*durationpb.Duration{
				"region-a": durationpb.New(25_000_000),
			}},
		}
	}
	created, err := client.CreateGameSessionCreationTicket(host, &mmpb.CreateGameSessionCreationTicketRequest{
		Parent: "tenants/current",
		GameSessionCreationTicket: &mmpb.GameSessionCreationTicket{
			MatchmakingConfig: "tenants/current/matchmakingConfigs/private_match_config",
			UserDefinitions:   []*mmpb.UserDefinition{definition("host-team")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.TrackGameSessionCreationTicket(host, &mmpb.TrackGameSessionCreationTicketRequest{Name: created.GetName()})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	joined, err := client.JoinGameSession(guest, &mmpb.JoinGameSessionRequest{
		Name:            opened.GetGameSession().GetName(),
		UserDefinitions: []*mmpb.UserDefinition{definition("guest-team")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		label   string
		matched []*mmpb.MatchedUserSession
	}{
		{"host-team", opened.GetMatchedUserSessions()},
		{"guest-team", joined.GetMatchedUserSessions()},
	} {
		if len(tc.matched) != 1 {
			t.Fatalf("%s matched sessions = %d", tc.label, len(tc.matched))
		}
		payload := decodeJWTSegment(t, strings.Split(tc.matched[0].GetMatchmakingIdToken(), ".")[1])
		gs := payload["gamesync"].(map[string]any)
		if gs["team"] != tc.label {
			t.Errorf("%s token team = %v", tc.label, gs["team"])
		}
		attr := map[string]any{}
		if err := json.Unmarshal([]byte(gs["attr"].(string)), &attr); err != nil {
			t.Fatal(err)
		}
		matchHash := attr["match_hash"].(map[string]any)
		if matchHash["value"] != tc.label {
			t.Errorf("%s token match_hash = %v", tc.label, matchHash)
		}
		ltcy := map[string]any{}
		if err := json.Unmarshal([]byte(gs["ltcy"].(string)), &ltcy); err != nil {
			t.Fatal(err)
		}
		region := ltcy["latencies"].(map[string]any)["region-a"].(map[string]any)
		if region["nanos"] != float64(25_000_000) {
			t.Errorf("%s token latency = %v", tc.label, region)
		}
	}
}

// TestARoomChecksItsPasswordAndItsCapacity: the password is checked on join,
// the only place the server can check it, and a full room refuses.
func TestARoomChecksItsPasswordAndItsCapacity(t *testing.T) {
	h := newHarness(t)
	h.createUser("host0000000000003", "device-host-3")
	host := h.ctx(h.sessionFor(t, "host0000000000003", "device-host-3"), "")
	client := mmpb.NewGameSessionServiceClient(h.conn)
	room := openRoom(t, h, host, "coop_private_config", nil, "1234").GetGameSession()

	h.createUser("guest000000000031", "device-guest-31")
	guest := h.ctx(h.sessionFor(t, "guest000000000031", "device-guest-31"), "")
	_, err := client.JoinGameSession(guest, &mmpb.JoinGameSessionRequest{Name: room.GetName(), Password: "0000"})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("wrong password: %v, want PermissionDenied", err)
	}

	if _, err := client.JoinGameSession(guest, &mmpb.JoinGameSessionRequest{Name: room.GetName(), Password: "1234"}); err != nil {
		t.Fatalf("join with the right password: %v", err)
	}
	for _, id := range []string{"guest000000000032", "guest000000000033"} {
		h.createUser(id, "device-"+id)
		ctx := h.ctx(h.sessionFor(t, id, "device-"+id), "")
		if _, err := client.JoinGameSession(ctx, &mmpb.JoinGameSessionRequest{Name: room.GetName(), Password: "1234"}); err != nil {
			t.Fatalf("join %s: %v", id, err)
		}
	}
	h.createUser("guest000000000034", "device-guest-34")
	late := h.ctx(h.sessionFor(t, "guest000000000034", "device-guest-34"), "")
	_, err = client.JoinGameSession(late, &mmpb.JoinGameSessionRequest{Name: room.GetName(), Password: "1234"})
	if status.Code(err) != codes.ResourceExhausted {
		t.Errorf("fifth player into a four-player room: %v, want ResourceExhausted", err)
	}
}

// TestCancellingARoomClosesIt: cancelling the ticket takes the room and its
// code with it, and only the host may cancel.
func TestCancellingARoomClosesIt(t *testing.T) {
	h := newHarness(t)
	h.createUser("host0000000000004", "device-host-4")
	h.createUser("other000000000004", "device-other-4")
	host := h.ctx(h.sessionFor(t, "host0000000000004", "device-host-4"), "")
	other := h.ctx(h.sessionFor(t, "other000000000004", "device-other-4"), "")
	client := mmpb.NewGameSessionServiceClient(h.conn)

	ticket := openRoom(t, h, host, "private_match_config", nil, "")
	alias, err := client.CreateGameSessionShortAlias(host, &mmpb.CreateGameSessionShortAliasRequest{
		GameSessionShortAlias: &mmpb.GameSessionShortAlias{GameSession: ticket.GetGameSession().GetName()},
	})
	if err != nil {
		t.Fatalf("CreateGameSessionShortAlias: %v", err)
	}

	if _, err := client.CancelGameSessionCreationTicket(other, &mmpb.CancelGameSessionCreationTicketRequest{Name: ticket.GetName()}); status.Code(err) != codes.NotFound {
		t.Errorf("cancel by someone else: %v, want NotFound", err)
	}
	if _, err := client.CancelGameSessionCreationTicket(host, &mmpb.CancelGameSessionCreationTicketRequest{Name: ticket.GetName()}); err != nil {
		t.Fatalf("cancel by the host: %v", err)
	}
	if _, err := client.GetGameSession(other, &mmpb.GetGameSessionRequest{Name: ticket.GetGameSession().GetName()}); status.Code(err) != codes.NotFound {
		t.Errorf("the room is still readable after being closed: %v", err)
	}
	if _, err := client.GetGameSessionShortAlias(other, &mmpb.GetGameSessionShortAliasRequest{Name: alias.GetName()}); status.Code(err) != codes.NotFound {
		t.Errorf("the room's code still resolves after it was closed: %v", err)
	}
}

// TestARoomCodeResolvesToItsRoomAndIsShownAsTheRoomID: the code the host
// shows friends resolves to the room, and _AliasSuffix -- where the game reads
// the Room ID it displays -- carries it.
func TestARoomCodeResolvesToItsRoomAndIsShownAsTheRoomID(t *testing.T) {
	h := newHarness(t)
	h.createUser("host0000000000005", "device-host-5")
	h.createUser("friend00000000005", "device-friend-5")
	host := h.ctx(h.sessionFor(t, "host0000000000005", "device-host-5"), "")
	friend := h.ctx(h.sessionFor(t, "friend00000000005", "device-friend-5"), "")
	client := mmpb.NewGameSessionServiceClient(h.conn)
	room := openRoom(t, h, host, "private_match_config", nil, "").GetGameSession()

	if _, err := client.CreateGameSessionShortAlias(friend, &mmpb.CreateGameSessionShortAliasRequest{
		GameSessionShortAlias: &mmpb.GameSessionShortAlias{GameSession: room.GetName()},
	}); status.Code(err) != codes.NotFound {
		t.Errorf("a code minted by someone outside the room: %v, want NotFound", err)
	}

	alias, err := client.CreateGameSessionShortAlias(host, &mmpb.CreateGameSessionShortAliasRequest{
		GameSessionShortAlias: &mmpb.GameSessionShortAlias{GameSession: room.GetName()},
	})
	if err != nil {
		t.Fatalf("CreateGameSessionShortAlias: %v", err)
	}
	code := lastSegment(alias.GetName())
	if len(code) != 6 || alias.GetExpireTime() == nil {
		t.Errorf("alias = %v, want a six-character code with an expiry", alias)
	}

	resolved, err := client.GetGameSessionShortAlias(friend, &mmpb.GetGameSessionShortAliasRequest{
		Name: "tenants/current/gameSessionShortAliases/" + strings.ToLower(code),
	})
	if err != nil {
		t.Fatalf("GetGameSessionShortAlias: %v", err)
	}
	if resolved.GetGameSession() != room.GetName() {
		t.Errorf("code resolves to %q, want %q", resolved.GetGameSession(), room.GetName())
	}

	seen, err := client.GetGameSession(friend, &mmpb.GetGameSessionRequest{Name: room.GetName()})
	if err != nil {
		t.Fatalf("GetGameSession: %v", err)
	}
	if got := seen.GetProperties().GetFields()["_AliasSuffix"].GetStringValue(); got != code {
		t.Errorf("_AliasSuffix = %q, want the room code %q", got, code)
	}
}

// TestAMatchIsStillHiddenFromOutsiders: opening rooms to anyone who names them
// must not open matched sessions the same way.
func TestAMatchIsStillHiddenFromOutsiders(t *testing.T) {
	h := newHarness(t)
	h.createUser("player-one", "device-one")
	h.createUser("player-two", "device-two")
	h.createUser("outsider00000001", "device-outsider")
	one := h.ctx(h.sessionFor(t, "player-one", "device-one"), "")
	two := h.ctx(h.sessionFor(t, "player-two", "device-two"), "")
	outsider := h.ctx(h.sessionFor(t, "outsider00000001", "device-outsider"), "")

	mm := mmpb.NewMatchmakerClient(h.conn)
	if _, err := mm.CreateMatchmakingTicket(one, ticketFor("turf-war")); err != nil {
		t.Fatalf("ticket one: %v", err)
	}
	second, err := mm.CreateMatchmakingTicket(two, ticketFor("turf-war"))
	if err != nil {
		t.Fatalf("ticket two: %v", err)
	}
	name := second.GetGameSession().GetName()
	client := mmpb.NewGameSessionServiceClient(h.conn)
	if _, err := client.GetGameSession(outsider, &mmpb.GetGameSessionRequest{Name: name}); status.Code(err) != codes.NotFound {
		t.Errorf("an outsider read a match: %v", err)
	}
	if _, err := client.JoinGameSession(outsider, &mmpb.JoinGameSessionRequest{Name: name}); status.Code(err) != codes.NotFound {
		t.Errorf("an outsider joined a match: %v", err)
	}
}
