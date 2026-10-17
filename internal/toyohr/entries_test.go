package toyohr

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/n-popescu/grid0plus/internal/db"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// liveFest saves an enabled fest and returns it, with the service's clock set
// so the fest is in the given phase.
func liveFest(t *testing.T, at time.Time) (*Service, *db.Memory, Fest) {
	t.Helper()
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	svc, store := newService(t, at)

	f := NewFest(start, DefaultRegions)
	if err := svc.SaveFest(context.Background(), f, true, nil); err != nil {
		t.Fatalf("save fest: %v", err)
	}
	return svc, store, f
}

func entryFor(t *testing.T, svc *Service, festID, team string) *toyohrpb.FestEntry {
	t.Helper()
	entry, err := svc.CreateFestEntry(context.Background(), &toyohrpb.CreateFestEntryRequest{
		Parent:    "tenants/" + testTenant + "/fests/" + festID,
		FestEntry: &toyohrpb.FestEntry{FestTeam: team, FestRegion: "eu"},
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	return entry
}

func TestAPlayerPicksATeamAndCanReadItBack(t *testing.T) {
	// Announced but not started: this is when a player picks.
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, announced)

	entry := entryFor(t, svc, f.ID, "alpha")
	if entry.GetFestTeam() != "alpha" {
		t.Errorf("team = %q, want alpha", entry.GetFestTeam())
	}
	// Uppercased on the way in, matching how regions are compared everywhere
	// else in this package.
	if entry.GetFestRegion() != "EU" {
		t.Errorf("region = %q, want EU", entry.GetFestRegion())
	}

	got, err := svc.GetFestEntry(context.Background(), &toyohrpb.GetFestEntryRequest{
		Name: "tenants/" + testTenant + "/fests/" + f.ID + "/festEntry",
	})
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if got.GetFestTeam() != "alpha" {
		t.Errorf("read back team %q, want alpha", got.GetFestTeam())
	}
}

func TestTheFirstPickIsFinalEvenBeforePlayOpens(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, store, f := liveFest(t, announced)
	var told []db.FestEntry
	svc.onEntry = func(_ context.Context, e db.FestEntry) { told = append(told, e) }

	entryFor(t, svc, f.ID, "alpha")
	// Splatoon 3 never lets a player change team once picked; the vote post
	// on Discord follows the same rule, so the server must too.
	again := entryFor(t, svc, f.ID, "bravo")
	if again.GetFestTeam() != "alpha" {
		t.Errorf("answered %q, want alpha — the first pick", again.GetFestTeam())
	}

	entries, err := store.ListFestEntries(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	// Two rows for one player would count them twice in the vote.
	if len(entries) != 1 || entries[0].Team != "alpha" {
		t.Fatalf("entries = %+v, want one alpha", entries)
	}
	// The bot hears about the new entry once, not about the refused change.
	if len(told) != 1 || told[0].Team != "alpha" {
		t.Errorf("OnEntry calls = %+v, want one for alpha", told)
	}
}

func TestAConsoleConfirmingABotPickedTeamFillsInItsRegion(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, store, f := liveFest(t, announced)
	if _, _, err := store.CreateFestEntry(context.Background(),
		db.FestEntry{FestID: f.ID, UserID: testCaller, Team: "alpha"}); err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, svc, f.ID, "alpha")
	if entry.GetFestRegion() != "EU" {
		t.Errorf("region = %q, want EU from the console", entry.GetFestRegion())
	}
}

func TestOncePlayOpensTheChoiceIsLocked(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, announced)
	entryFor(t, svc, f.ID, "alpha")

	// Play has started. Switching sides now would move a vote that has already
	// been counted into a published ratio.
	svc.now = func() time.Time { return f.Start.Add(time.Hour) }

	entry, err := svc.CreateFestEntry(context.Background(), &toyohrpb.CreateFestEntryRequest{
		Parent:    "tenants/" + testTenant + "/fests/" + f.ID,
		FestEntry: &toyohrpb.FestEntry{FestTeam: "bravo"},
	})
	if err != nil {
		// Not an error: a console re-sending its choice on reconnect is normal,
		// and an error there reads as the fest being broken.
		t.Fatalf("re-entering after play opened: %v", err)
	}
	if entry.GetFestTeam() != "alpha" {
		t.Errorf("team = %q, want alpha — the locked choice, not the new one", entry.GetFestTeam())
	}
}

func TestAPlayerWhoNeverPickedCanStillJoinWhilePlayIsOpen(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, announced)
	svc.now = func() time.Time { return f.Start.Add(time.Hour) }

	// The game asks a player who skipped the announcement to pick a team in
	// the first half; refusing it is a communication error.
	entry, err := svc.CreateFestEntry(context.Background(), &toyohrpb.CreateFestEntryRequest{
		Parent:    "tenants/" + testTenant + "/fests/" + f.ID,
		FestEntry: &toyohrpb.FestEntry{FestTeam: "alpha"},
	})
	if err != nil || entry.GetFestTeam() != "alpha" {
		t.Fatalf("a late entry gave %v, %v; want the entry", entry, err)
	}

	svc.now = func() time.Time { return f.Timetable().End.Add(time.Minute) }
	_, err = svc.CreateFestEntry(context.Background(), &toyohrpb.CreateFestEntryRequest{
		Parent:    "tenants/" + testTenant + "/fests/" + f.ID,
		FestEntry: &toyohrpb.FestEntry{FestTeam: "beta"},
	})
	if err != nil {
		t.Fatalf("the existing entry should be returned after play: %v", err)
	}
}

func TestAnEntryAfterPlayEndsIsRefused(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, announced)
	svc.now = func() time.Time { return f.Timetable().End.Add(time.Minute) }

	_, err := svc.CreateFestEntry(context.Background(), &toyohrpb.CreateFestEntryRequest{
		Parent:    "tenants/" + testTenant + "/fests/" + f.ID,
		FestEntry: &toyohrpb.FestEntry{FestTeam: "alpha"},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("an entry after play ended gave %v, want FailedPrecondition", err)
	}
}

func TestAnUnannouncedOrDisabledFestDoesNotExistToAConsole(t *testing.T) {
	// Before the announcement. Nobody should be able to see it, let alone vote.
	beforeAnnounce := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, beforeAnnounce)

	_, err := svc.CreateFestEntry(context.Background(), &toyohrpb.CreateFestEntryRequest{
		Parent:    "tenants/" + testTenant + "/fests/" + f.ID,
		FestEntry: &toyohrpb.FestEntry{FestTeam: "alpha"},
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("voting in an unannounced fest gave %v, want NotFound", err)
	}

	// A scheduled-but-disabled fest is how the operator holds one ready. To a
	// console it does not exist.
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc2, store, f2 := liveFest(t, announced)
	row, _ := store.GetFest(context.Background(), f2.ID)
	row.Enabled = false
	if err := store.UpsertFest(context.Background(), row); err != nil {
		t.Fatalf("disable fest: %v", err)
	}
	_, err = svc2.CreateFestEntry(context.Background(), &toyohrpb.CreateFestEntryRequest{
		Parent:    "tenants/" + testTenant + "/fests/" + f2.ID,
		FestEntry: &toyohrpb.FestEntry{FestTeam: "alpha"},
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("voting in a disabled fest gave %v, want NotFound", err)
	}
}

func TestAnEntryWithNoTeamIsRefused(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, announced)

	// An empty team would show up in the tally as its own side.
	_, err := svc.CreateFestEntry(context.Background(), &toyohrpb.CreateFestEntryRequest{
		Parent:    "tenants/" + testTenant + "/fests/" + f.ID,
		FestEntry: &toyohrpb.FestEntry{FestTeam: "  "},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("an empty team gave %v, want InvalidArgument", err)
	}
}

func TestAnUnauthenticatedCallCannotVoteOrRead(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, announced)

	as(t, "")

	parent := "tenants/" + testTenant + "/fests/" + f.ID
	if _, err := svc.CreateFestEntry(context.Background(), &toyohrpb.CreateFestEntryRequest{
		Parent: parent, FestEntry: &toyohrpb.FestEntry{FestTeam: "alpha"},
	}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("voting with no caller gave %v, want Unauthenticated", err)
	}
	if _, err := svc.GetFestEntry(context.Background(), &toyohrpb.GetFestEntryRequest{
		Name: parent + "/festEntry",
	}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("reading with no caller gave %v, want Unauthenticated", err)
	}
	if _, err := svc.GetFestResult(context.Background(), &toyohrpb.GetFestResultRequest{
		Name: parent + "/result",
	}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("reading a result with no caller gave %v, want Unauthenticated", err)
	}
}

func TestAServiceWithNoCallerResolverRefusesRatherThanGuessing(t *testing.T) {
	// The dashboard builds a Service to schedule fests and never serves an RPC
	// through it. Fail-closed, so a handler reached that way refuses.
	svc := NewService(Options{Store: db.NewMemory(), Log: discardLogger(), Tenant: testTenant})

	_, err := svc.GetFestEntry(context.Background(), &toyohrpb.GetFestEntryRequest{Name: "NXTD-00001"})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("a handler with no caller resolver gave %v, want Unauthenticated", err)
	}
}

func TestTheVoteIsMeasuredAndTheBattleRatiosAreReportedAsNotAvailable(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, announced)

	// Three for alpha, one for bravo.
	for _, who := range []string{"p1", "p2", "p3"} {
		as(t, who)
		entryFor(t, svc, f.ID, "Alpha")
	}
	as(t, "p4")
	entryFor(t, svc, f.ID, "Bravo")
	as(t, "reader")
	svc.now = func() time.Time { return f.Timetable().Start.Add(time.Hour) }

	result, err := svc.GetFestResult(context.Background(), &toyohrpb.GetFestResultRequest{
		Name: "tenants/" + testTenant + "/fests/" + f.ID + "/result",
	})
	if err != nil {
		t.Fatalf("get result: %v", err)
	}

	// Yobisai is the Conch Shells share on screen, and GRID0+ counts no
	// shells: valid (somebody entered), every team listed, all at 0 -- not
	// the vote, which a lone voter read as 99% of the shells on hardware.
	yobisai := result.GetYobisai()
	if !yobisai.GetIsValid() {
		t.Error("Yobisai is reported as not available once somebody has entered")
	}
	ratios := yobisai.GetTeamRatios()
	if len(ratios) != 3 {
		t.Fatalf("got %d team ratios, want all 3 protocol teams", len(ratios))
	}
	for i, want := range []string{"Alpha", "Bravo", "Charlie"} {
		if ratios[i].GetFestTeam() != want || ratios[i].GetRatio() != 0 {
			t.Errorf("ratio %d = %s %.2f, want %s 0", i, ratios[i].GetFestTeam(), ratios[i].GetRatio(), want)
		}
	}

	// Battle ratios need battle records, and nothing writes those yet. Reported
	// as not available rather than filled with a plausible number: a fabricated
	// ratio is indistinguishable from a measured one to whoever reads it next.
	if result.GetHonsaiMidterm().GetIsValid() || result.GetHonsaiFinal().GetIsValid() {
		t.Error("a battle ratio is reported as valid, but no battle is recorded anywhere")
	}

	// And no verdict mid-fest: publishing one would announce a winner while the
	// fest is still open.
	if result.GetOverall().GetIsValid() {
		t.Error("a verdict was published while the fest was still open")
	}
	if result.GetHonsaiMidterm() != nil || result.GetHonsaiFinal() != nil {
		t.Error("live-fest result must omit phases that have not been played")
	}
	if result.GetOverall().GetWeights() == nil || result.GetUnk() != 3 {
		t.Errorf("live-fest result has overall=%v unk=%d, want empty weights envelope and 3 teams", result.GetOverall(), result.GetUnk())
	}
	raw, err := proto.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	wireFields := map[protowire.Number]bool{}
	for len(raw) > 0 {
		field, kind, tagLen := protowire.ConsumeTag(raw)
		if tagLen < 0 {
			t.Fatal("invalid FestResult wire tag")
		}
		raw = raw[tagLen:]
		valueLen := protowire.ConsumeFieldValue(field, kind, raw)
		if valueLen < 0 {
			t.Fatal("invalid FestResult wire value")
		}
		wireFields[field] = true
		raw = raw[valueLen:]
	}
	if wireFields[3] || wireFields[4] || !wireFields[2] || !wireFields[5] || !wireFields[6] {
		t.Errorf("first-half FestResult wire fields = %v, want yobisai/overall/unk and no midterm/final", wireFields)
	}
}

func TestSecondHalfPublishesRankedMidpointWithoutFinalResult(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, announced)
	for _, choice := range []struct{ user, team string }{
		{"p1", "Bravo"}, {"p2", "Bravo"}, {"p3", "Alpha"},
	} {
		as(t, choice.user)
		entryFor(t, svc, f.ID, choice.team)
	}
	as(t, "reader")
	svc.now = func() time.Time { return f.Timetable().Mid.Add(time.Hour) }
	result, err := svc.GetFestResult(context.Background(), &toyohrpb.GetFestResultRequest{
		Name: "tenants/" + testTenant + "/festResults/" + f.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetYobisai().GetIsValid() || !result.GetHonsaiMidterm().GetIsValid() {
		t.Fatalf("second half must contain valid preliminary and midpoint results: %v", result)
	}
	if result.GetHonsaiFinal() != nil || result.GetOverall().GetIsValid() || result.GetOverall().GetWeights() == nil {
		t.Fatalf("second half must omit the final and carry an empty overall weights envelope: %v", result)
	}
	preliminary := result.GetYobisai().GetTeamRatios()
	midpoint := result.GetHonsaiMidterm().GetTeamRatios()
	if len(preliminary) != 3 || len(midpoint) != 3 || result.GetUnk() != 3 {
		t.Fatalf("second half must include all three teams: %v", result)
	}
	if preliminary[0].GetFestTeam() != "Alpha" || midpoint[0].GetFestTeam() != "Bravo" ||
		midpoint[0].GetRatio() != 2.0/3 || midpoint[1].GetFestTeam() != "Alpha" ||
		midpoint[2].GetFestTeam() != "Charlie" {
		t.Fatalf("midpoint should rank vote shares without reordering preliminary results: %v", result)
	}
}

func TestGetFestResultAcceptsTheFestResultsResourceAndEchoesItsName(t *testing.T) {
	started := time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, started)
	requestedName := "tenants/current/festResults/" + f.ID

	result, err := svc.GetFestResult(context.Background(), &toyohrpb.GetFestResultRequest{
		Name: requestedName,
	})
	if err != nil {
		t.Fatalf("GetFestResult(%q): %v", requestedName, err)
	}
	if result.GetName() != requestedName {
		t.Errorf("result name = %q, want request name %q", result.GetName(), requestedName)
	}
}

func TestGetFestResultReturnsAnEmptyNamedResultForAnUnrecognizedResource(t *testing.T) {
	svc, _, _ := liveFest(t, time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC))
	requestedName := "tenants/current/festResults"

	result, err := svc.GetFestResult(context.Background(), &toyohrpb.GetFestResultRequest{
		Name: requestedName,
	})
	if err != nil {
		t.Fatalf("GetFestResult(%q): %v, want empty result", requestedName, err)
	}
	if result.GetName() != requestedName {
		t.Errorf("empty result name = %q, want request name %q", result.GetName(), requestedName)
	}
	if result.GetYobisai() != nil || result.GetOverall() != nil {
		t.Errorf("unrecognized resource returned result data: %v", result)
	}
}

func TestTheVerdictIsPublishedOnlyOncePlayIsOver(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, announced)

	as(t, "p1")
	entryFor(t, svc, f.ID, "Alpha")
	as(t, "p2")
	entryFor(t, svc, f.ID, "Alpha")
	as(t, "p3")
	entryFor(t, svc, f.ID, "Bravo")
	as(t, "reader")
	svc.now = func() time.Time { return f.Timetable().End.Add(time.Minute) }
	pending, err := svc.GetFestResult(context.Background(), &toyohrpb.GetFestResultRequest{
		Name: "tenants/" + testTenant + "/festResults/" + f.ID,
	})
	if err != nil || pending.GetOverall().GetIsValid() || pending.GetHonsaiFinal() != nil {
		t.Fatalf("counting phase exposed a final verdict: %v, %v", pending, err)
	}

	// The result window opens after counting; publishing before it would show
	// the winner while Nintendo's phase still says results are pending.
	svc.now = func() time.Time { return f.Timetable().Close.Add(time.Minute) }

	result, err := svc.GetFestResult(context.Background(), &toyohrpb.GetFestResultRequest{
		Name: "tenants/" + testTenant + "/fests/" + f.ID + "/result",
	})
	if err != nil {
		t.Fatalf("get result: %v", err)
	}
	overall := result.GetOverall()
	if !overall.GetIsValid() {
		t.Fatal("no verdict once play is over, so the fest never concludes")
	}
	points := overall.GetPoints()
	if len(points) != 3 {
		t.Fatalf("got %d team points, want 3", len(points))
	}
	// No battle was played, so only the vote scores: no category stands in
	// for games nobody played, and Conch Shells score nothing.
	if points[0].GetFestTeam() != "Alpha" || points[0].GetPoints() != 70 {
		t.Errorf("winner = %s with %d, want Alpha with 70",
			points[0].GetFestTeam(), points[0].GetPoints())
	}
}

func TestAFestNobodyEnteredHasNoVerdictRatherThanATie(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, announced)
	svc.now = func() time.Time { return f.Timetable().Close.Add(time.Hour) }

	result, err := svc.GetFestResult(context.Background(), &toyohrpb.GetFestResultRequest{
		Name: "tenants/" + testTenant + "/fests/" + f.ID + "/result",
	})
	if err != nil {
		t.Fatalf("get result: %v", err)
	}
	// A tie is a result; this is the absence of one.
	if result.GetOverall().GetIsValid() {
		t.Error("a fest nobody entered published a verdict")
	}
	if result.GetYobisai().GetIsValid() {
		t.Error("an empty vote is reported as valid")
	}
}

func TestDeletingAFestTakesItsVotesWithIt(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, store, f := liveFest(t, announced)
	entryFor(t, svc, f.ID, "alpha")

	if err := store.DeleteFest(context.Background(), f.ID); err != nil {
		t.Fatalf("delete fest: %v", err)
	}

	// Otherwise recreating the same fest id inherits the old votes.
	entries, err := store.ListFestEntries(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("%d votes outlived the fest they belonged to", len(entries))
	}
}
