package toyohr

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/n-popescu/switchnet/internal/db"
	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
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

func TestChangingTeamBeforePlayOpensReplacesTheChoiceRatherThanVotingTwice(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, store, f := liveFest(t, announced)

	entryFor(t, svc, f.ID, "alpha")
	entryFor(t, svc, f.ID, "bravo")

	entries, err := store.ListFestEntries(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	// Two rows for one player would count them twice in the vote.
	if len(entries) != 1 {
		t.Fatalf("the player has %d entries, want 1", len(entries))
	}
	if entries[0].Team != "bravo" {
		t.Errorf("team = %q, want bravo — the later choice", entries[0].Team)
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

func TestAPlayerWhoNeverPickedCannotJoinAfterPlayOpens(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, announced)
	svc.now = func() time.Time { return f.Start.Add(time.Hour) }

	// A vote arriving after a ratio was published changes a number the players
	// have already seen.
	_, err := svc.CreateFestEntry(context.Background(), &toyohrpb.CreateFestEntryRequest{
		Parent:    "tenants/" + testTenant + "/fests/" + f.ID,
		FestEntry: &toyohrpb.FestEntry{FestTeam: "alpha"},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("a late entry gave %v, want FailedPrecondition", err)
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
		entryFor(t, svc, f.ID, "alpha")
	}
	as(t, "p4")
	entryFor(t, svc, f.ID, "bravo")
	as(t, "reader")

	result, err := svc.GetFestResult(context.Background(), &toyohrpb.GetFestResultRequest{
		Name: "tenants/" + testTenant + "/fests/" + f.ID + "/result",
	})
	if err != nil {
		t.Fatalf("get result: %v", err)
	}

	// The vote is real data, so it is valid and it is right.
	yobisai := result.GetYobisai()
	if !yobisai.GetIsValid() {
		t.Error("the vote is reported as not available, but it is the one thing measured")
	}
	ratios := yobisai.GetTeamRatios()
	if len(ratios) != 2 {
		t.Fatalf("got %d team ratios, want 2", len(ratios))
	}
	// Largest first, so the leading side is not buried.
	if ratios[0].GetFestTeam() != "alpha" || ratios[0].GetRatio() != 0.75 {
		t.Errorf("first ratio = %s %.2f, want alpha 0.75",
			ratios[0].GetFestTeam(), ratios[0].GetRatio())
	}
	if ratios[1].GetFestTeam() != "bravo" || ratios[1].GetRatio() != 0.25 {
		t.Errorf("second ratio = %s %.2f, want bravo 0.25",
			ratios[1].GetFestTeam(), ratios[1].GetRatio())
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
}

func TestTheVerdictIsPublishedOnlyOncePlayIsOver(t *testing.T) {
	announced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc, _, f := liveFest(t, announced)

	as(t, "p1")
	entryFor(t, svc, f.ID, "alpha")
	as(t, "p2")
	entryFor(t, svc, f.ID, "alpha")
	as(t, "p3")
	entryFor(t, svc, f.ID, "bravo")
	as(t, "reader")

	// Counting: play has ended, results are not out yet.
	svc.now = func() time.Time { return f.Timetable().End.Add(time.Minute) }

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
	if len(points) != 2 {
		t.Fatalf("got %d team points, want 2", len(points))
	}
	if points[0].GetFestTeam() != "alpha" || points[0].GetPoints() != 2 {
		t.Errorf("winner = %s with %d, want alpha with 2",
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
