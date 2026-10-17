package baas

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/presencebus"
)

const (
	modDiscord   = "100000000000000009"
	carolDiscord = "100000000000000003"
	daveDiscord  = "100000000000000004"
)

var testTeams = []map[string]string{
	{"name": "Cookies", "emoji": "🍪", "roleId": "200000000000000001"},
	{"name": "Snowmen", "emoji": "⛄", "roleId": "200000000000000002"},
}

// newFest creates a Discord-only fest whose play opened an hour ago.
func (b *botHarness) newFest() string {
	b.t.Helper()
	resp, body := b.call("POST", "/bot/v1/fests", modDiscord, map[string]any{
		"title": "Frostyfest", "description": "Cookies or snowmen?",
		"startAt": b.clock().Add(-time.Hour), "teams": testTeams,
	})
	if resp.StatusCode != http.StatusCreated {
		b.t.Fatalf("create fest: %d %v", resp.StatusCode, body)
	}
	return body["festId"].(string)
}

func (b *botHarness) vote(festID, discordID, team string) (int, map[string]any) {
	b.t.Helper()
	resp, body := b.call("POST", "/bot/v1/fests/"+festID+"/entry", discordID, map[string]string{"team": team})
	return resp.StatusCode, body
}

// A Discord-only fest must never reach a console: it is a fest row that is
// never enabled and has no pack, which every console-facing path skips.
func TestADiscordOnlyFestIsADisabledFestTheGameNeverSees(t *testing.T) {
	b := newBotHarness(t)
	id := b.newFest()
	if !strings.HasPrefix(id, "LANF-") {
		t.Errorf("fest id %q, want a LANF- id that cannot collide with an in-game one", id)
	}
	row, err := b.store.GetFest(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Enabled || row.Resource != "" {
		t.Errorf("fest row = %+v, want disabled with no pack", row)
	}
	_, got := b.call("GET", "/bot/v1/fests/"+id, "", nil)
	teams, _ := got["teams"].([]any)
	if got["phase"] != "first_half" || got["discordOnly"] != true || len(teams) != 2 ||
		teams[0].(map[string]any)["team"] != "Alpha" || teams[1].(map[string]any)["team"] != "Bravo" {
		t.Errorf("fest = %v", got)
	}
}

func TestAFestPostLinkedToAnInGameFestNeedsThatFestAndItsThreeTeams(t *testing.T) {
	b := newBotHarness(t)
	resp, body := b.call("POST", "/bot/v1/fests", modDiscord, map[string]any{
		"festId": "JUEA-00001", "title": "x", "teams": append(testTeams,
			map[string]string{"name": "Ornaments", "emoji": "🎄", "roleId": "200000000000000003"}),
	})
	if resp.StatusCode != http.StatusNotFound || body["code"] != "no_such_fest" {
		t.Errorf("link to a missing fest: %d %v", resp.StatusCode, body)
	}
	if err := b.store.UpsertFest(t.Context(), db.Fest{ID: "JUEA-00001", Resource: "juea-test",
		StartAt: b.clock().Add(48 * time.Hour), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	resp, body = b.call("POST", "/bot/v1/fests", modDiscord, map[string]any{
		"festId": "JUEA-00001", "title": "x", "teams": testTeams,
	})
	if resp.StatusCode != http.StatusBadRequest || body["code"] != "bad_teams" {
		t.Errorf("an in-game fest with two teams: %d %v", resp.StatusCode, body)
	}
}

// The first pick is final, from Discord as from a console, and only a
// registered player can vote.
func TestAVoteFromThePostIsFinalAndNeedsAnAccount(t *testing.T) {
	b := newBotHarness(t)
	id := b.newFest()
	if code, body := b.vote(id, aliceDiscord, "Alpha"); code != http.StatusNotFound || body["code"] != "not_registered" {
		t.Errorf("unregistered vote: %d %v", code, body)
	}
	b.registerDiscord(aliceDiscord, "alice")
	if code, body := b.vote(id, aliceDiscord, "Alpha"); code != http.StatusOK || body["created"] != true {
		t.Fatalf("first vote: %d %v", code, body)
	}
	if code, body := b.vote(id, aliceDiscord, "Bravo"); code != http.StatusOK || body["created"] != false || body["team"] != "Alpha" {
		t.Errorf("second vote replaced the first: %d %v", code, body)
	}
	if code, body := b.vote(id, aliceDiscord, "Charlie"); code != http.StatusBadRequest || body["code"] != "bad_team" {
		t.Errorf("vote for a team this fest does not have: %d %v", code, body)
	}
	_, got := b.call("GET", "/bot/v1/fests/"+id+"/entries", "", nil)
	entries, _ := got["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["discordUserId"] != aliceDiscord {
		t.Errorf("entries = %v", got)
	}
}

// fourPlayers registers alice and carol on Alpha, bob and dave on Bravo.
func (b *botHarness) fourPlayers(id string) {
	b.t.Helper()
	for _, p := range []struct{ discord, name, team string }{
		{aliceDiscord, "alice", "Alpha"}, {carolDiscord, "carol", "Alpha"},
		{bobDiscord, "bob", "Bravo"}, {daveDiscord, "dave", "Bravo"},
	} {
		b.registerDiscord(p.discord, p.name)
		if code, body := b.vote(id, p.discord, p.team); code != http.StatusOK {
			b.t.Fatalf("vote %s: %d %v", p.name, code, body)
		}
	}
}

func TestALANGameIsCheckedBeforeAModSeesIt(t *testing.T) {
	b := newBotHarness(t)
	id := b.newFest()
	b.fourPlayers(id)
	path := "/bot/v1/fests/" + id + "/submissions"

	resp, body := b.call("POST", path, modDiscord, map[string]any{
		"winners": []string{aliceDiscord, carolDiscord}, "losers": []string{bobDiscord, daveDiscord}})
	if resp.StatusCode != http.StatusForbidden || body["code"] != "not_a_player" {
		t.Errorf("a non-player submitting: %d %v", resp.StatusCode, body)
	}
	resp, body = b.call("POST", path, aliceDiscord, map[string]any{
		"winners": []string{aliceDiscord, bobDiscord}, "losers": []string{daveDiscord}})
	if resp.StatusCode != http.StatusConflict || body["code"] != "team_mismatch" {
		t.Errorf("a side mixing two teams: %d %v", resp.StatusCode, body)
	}
	const stranger = "100000000000000077"
	resp, body = b.call("POST", path, aliceDiscord, map[string]any{
		"winners": []string{aliceDiscord}, "losers": []string{stranger}})
	ids, _ := body["discordUserIds"].([]any)
	if resp.StatusCode != http.StatusConflict || body["code"] != "players_not_registered" || len(ids) != 1 || ids[0] != stranger {
		t.Errorf("an unregistered player: %d %v", resp.StatusCode, body)
	}
	resp, body = b.call("POST", path, aliceDiscord, map[string]any{
		"winners": []string{aliceDiscord}, "losers": []string{bobDiscord}, "paint": map[string]int{bobDiscord: 99999}})
	if resp.StatusCode != http.StatusBadRequest || body["code"] != "bad_paint" {
		t.Errorf("impossible paint: %d %v", resp.StatusCode, body)
	}
}

// An accepted LAN game is an ordinary fest battle in the Open category, with
// the paint the players gave and the online average for the rest; nothing
// counts before a mod accepts it, and a mod cannot settle it twice.
func TestAnAcceptedLANGameCountsLikeAnOnlineWin(t *testing.T) {
	b := newBotHarness(t)
	id := b.newFest()
	b.fourPlayers(id)
	ctx := t.Context()

	// One online battle so far: winners painted 1500 and 1700, losers 800.
	alice, _ := b.store.GetUserByDiscordID(ctx, aliceDiscord)
	bob, _ := b.store.GetUserByDiscordID(ctx, bobDiscord)
	carol, _ := b.store.GetUserByDiscordID(ctx, carolDiscord)
	if err := b.store.RecordFestBattle(ctx, db.FestBattle{FestID: id, BattleID: "online-1", Mode: "fest_match_regular_normal",
		WinningTeam: "Alpha", FinishedAt: b.clock()}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []db.FestBattlePlayer{
		{UserID: alice.ID, Team: "Alpha", Paint: 1500}, {UserID: carol.ID, Team: "Alpha", Paint: 1700},
		{UserID: bob.ID, Team: "Bravo", Paint: 800},
	} {
		p.FestID, p.BattleID = id, "online-1"
		if err := b.store.RecordFestBattlePlayer(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	resp, sub := b.call("POST", "/bot/v1/fests/"+id+"/submissions", bobDiscord, map[string]any{
		"winners": []string{bobDiscord, daveDiscord}, "losers": []string{aliceDiscord, carolDiscord},
		"paint": map[string]int{bobDiscord: 1234},
	})
	if resp.StatusCode != http.StatusCreated || sub["status"] != "pending" || sub["winningTeam"] != "Bravo" {
		t.Fatalf("submit: %d %v", resp.StatusCode, sub)
	}
	subID := sub["id"].(string)
	if battles, _ := b.store.ListFestBattles(ctx, id); len(battles) != 1 {
		t.Fatalf("a pending game was counted: %d battles", len(battles))
	}

	resp, flagged := b.call("POST", "/bot/v1/submissions/"+subID+"/flag", aliceDiscord, map[string]string{"reason": "we won"})
	if resp.StatusCode != http.StatusOK || len(flagged["flags"].([]any)) != 1 {
		t.Errorf("flag: %d %v", resp.StatusCode, flagged)
	}
	resp, body := b.call("POST", "/bot/v1/submissions/"+subID+"/refuse", modDiscord, map[string]string{})
	if resp.StatusCode != http.StatusBadRequest || body["code"] != "bad_reason" {
		t.Errorf("a refusal with no reason: %d %v", resp.StatusCode, body)
	}

	resp, accepted := b.call("POST", "/bot/v1/submissions/"+subID+"/accept", modDiscord, nil)
	if resp.StatusCode != http.StatusOK || accepted["status"] != "accepted" || accepted["reviewedBy"] != modDiscord {
		t.Fatalf("accept: %d %v", resp.StatusCode, accepted)
	}
	resp, again := b.call("POST", "/bot/v1/submissions/"+subID+"/refuse", modDiscord, map[string]string{"reason": "late"})
	if resp.StatusCode != http.StatusConflict || again["code"] != "already_reviewed" {
		t.Errorf("refusing an accepted game: %d %v", resp.StatusCode, again)
	}

	battles, _ := b.store.ListFestBattles(ctx, id)
	var lan db.FestBattle
	for _, bt := range battles {
		if bt.BattleID == "lan-"+subID {
			lan = bt
		}
	}
	// Bravo: bob gave 1234, dave gets the online winners' mean (1600).
	// Alpha: both get the online losers' mean (800).
	if lan.WinningTeam != "Bravo" || !strings.Contains(lan.Mode, "regular") ||
		lan.Ink["Bravo"] != 1234+1600 || lan.Ink["Alpha"] != 1600 {
		t.Errorf("LAN battle = %+v", lan)
	}

	_, me := b.call("GET", "/bot/v1/fests/"+id+"/me", bobDiscord, nil)
	lanTally, _ := me["lan"].(map[string]any)
	online, _ := me["online"].(map[string]any)
	if me["team"] != "Bravo" || lanTally["wins"] != float64(1) || lanTally["contribution"] != float64(1234) ||
		online["games"] != float64(1) || online["wins"] != float64(0) {
		t.Errorf("bob's record = %v", me)
	}
}

// Nobody reads a tally on Discord before the game would show one.
func TestStandingsFollowWhatTheGameShows(t *testing.T) {
	b := newBotHarness(t)
	id := b.newFest()
	b.fourPlayers(id)

	_, st := b.call("GET", "/bot/v1/fests/"+id+"/standings", "", nil)
	if st["phase"] != "first_half" || st["midterm"] != nil || st["final"] != nil {
		t.Errorf("first half standings = %v, want nothing", st)
	}
	b.advance(30 * time.Hour)
	_, st = b.call("GET", "/bot/v1/fests/"+id+"/standings", "", nil)
	if st["phase"] != "second_half" || st["midterm"] == nil || st["final"] != nil {
		t.Errorf("second half standings = %v, want the halftime ranking only", st)
	}
	b.advance(30 * time.Hour)
	_, st = b.call("GET", "/bot/v1/fests/"+id+"/standings", "", nil)
	final, _ := st["final"].(map[string]any)
	if st["phase"] != "closed" || final == nil || final["points"] == nil {
		t.Errorf("closed standings = %v, want the result", st)
	}
}

// The stream tells the bot about an in-game vote by a main account, and
// nothing about an extra account's: roles follow the main account only.
func TestAnInGameVoteReachesTheBotStream(t *testing.T) {
	h := newHarness(t)
	bus := presencebus.NewLocal()
	bot := httptest.NewServer(h.srv.BotHandler(BotAPIOptions{Token: testBotToken, FestBus: bus}))
	t.Cleanup(bot.Close)
	b := &botHarness{harness: h, bot: bot}
	id := b.newFest()
	b.registerDiscord(aliceDiscord, "alice")
	resp, extra := b.call("POST", "/bot/v1/register", "", map[string]any{
		"discordUserId": aliceDiscord, "username": "alice2", "additional": true})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("extra account: %d %v", resp.StatusCode, extra)
	}
	alice, _ := b.store.GetUserByDiscordID(t.Context(), aliceDiscord)

	req, _ := http.NewRequestWithContext(t.Context(), "GET", bot.URL+"/bot/v1/fest-events", nil)
	req.Header.Set("Authorization", "Bearer "+testBotToken)
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	lines := bufio.NewScanner(stream.Body)
	next := func() map[string]string {
		if !lines.Scan() {
			t.Fatalf("stream ended: %v", lines.Err())
		}
		var out map[string]string
		_ = json.Unmarshal(lines.Bytes(), &out)
		return out
	}
	if hello := next(); hello["type"] != "hello" {
		t.Fatalf("first line = %v", hello)
	}
	presencebus.PublishFestEntry(bus, "test", id, extra["userId"].(string), "Bravo", nil)
	presencebus.PublishFestEntry(bus, "test", id, alice.ID, "Alpha", nil)
	got := next()
	if got["type"] != "fest_entry" || got["discordUserId"] != aliceDiscord || got["team"] != "Alpha" || got["festId"] != id {
		t.Errorf("event = %v, want alice's main-account vote only", got)
	}
}

func TestTheFestStreamSaysSoWithoutRedis(t *testing.T) {
	b := newBotHarness(t)
	resp, body := b.call("GET", "/bot/v1/fest-events", "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable || body["code"] != "no_event_bus" {
		t.Errorf("stream without a bus: %d %v", resp.StatusCode, body)
	}
}
