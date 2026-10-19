package toyohr

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/n-popescu/grid0plus/internal/bcat"
	"github.com/n-popescu/grid0plus/internal/db"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

const testTenant = "dce9377b"

// testCaller is whoever the tests are acting as. A package variable so a test
// can act as a second player without rebuilding the service.
var testCaller = "player-1"

// as makes the tests act as a given player for the rest of the test.
//
// Restored on cleanup rather than by hand: a test that forgets to put it back
// changes the player every later test runs as, and the failure lands somewhere
// unrelated.
func as(t *testing.T, userID string) {
	t.Helper()
	previous := testCaller
	testCaller = userID
	t.Cleanup(func() { testCaller = previous })
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newService(t *testing.T, now time.Time) (*Service, *db.Memory) {
	t.Helper()
	store := db.NewMemory()
	svc := NewService(Options{
		Store:  store,
		Log:    discardLogger(),
		Tenant: testTenant,
		Now:    func() time.Time { return now },
		CallerID: func(context.Context) (string, bool) {
			return testCaller, testCaller != ""
		},
	})
	return svc, store
}

// TestTheAnnouncedFestNamesThePackAndRevisionTheConsoleMustLoad.
//
// The schedule carries no content at all: the team names, banners and idol
// dialogue are inside the BCAT pack. What it does carry is which pack, and
// which revision of it — and those two, plus the fest id, are what the console
// checks against its own BCAT cache. A mismatch is BcatInvalid.
func TestTheAnnouncedFestNamesThePackAndRevisionTheConsoleMustLoad(t *testing.T) {
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	// Team selection is open, but matches have not started.
	svc, _ := newService(t, start.Add(-24*time.Hour))

	f := NewFest(start, []string{"JP", "EU"})
	if err := svc.SaveFest(context.Background(), f, true, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	resp, err := svc.SelectFestSchedule(context.Background(),
		&toyohrpb.SelectFestScheduleRequest{Tenant: testTenant, Region: "EU"})
	if err != nil {
		t.Fatalf("SelectFestSchedule: %v", err)
	}

	sched := resp.GetSchedule()
	if sched == nil {
		t.Fatal("no schedule announced during a running fest")
	}
	if sched.GetFestGameData() != f.Resource {
		t.Errorf("pack = %q, want %q", sched.GetFestGameData(), f.Resource)
	}
	if sched.GetFestGameDataRevision() != f.Revision {
		t.Errorf("revision = %q, want %q", sched.GetFestGameDataRevision(), f.Revision)
	}
	if got := sched.GetTarget(); got != "default" {
		t.Errorf("target = %q, want default for an omitted request target", got)
	}
	withResourceTarget, err := svc.SelectFestSchedule(context.Background(),
		&toyohrpb.SelectFestScheduleRequest{Tenant: testTenant, Region: "EU", Target: "tenants/current/targets/default"})
	if err != nil {
		t.Fatalf("SelectFestSchedule with resource target: %v", err)
	}
	if got := withResourceTarget.GetSchedule().GetTarget(); got != "default" {
		t.Errorf("target = %q, want default for a resource target", got)
	}
	stages := sched.GetAttributes().GetFields()["TricolorStages"].GetArrayValue().GetValues()
	if len(stages) != 2 || stages[0].GetIntegerValue() != 2 || stages[1].GetIntegerValue() != 12 {
		t.Errorf("TricolorStages = %v, want captured two-stage attribute [2, 12]", stages)
	}
	// The fest id has to appear in the resource names, because that is what the
	// console matches against its cache metadata.
	if want := "/fests/" + f.ID; !strings.Contains(sched.GetFest(), want) {
		t.Errorf("fest name %q does not contain %q", sched.GetFest(), want)
	}
}

// TestTheAnnouncedTimetableIsSlotAlignedOnTheWire.
//
// fest_test.go pins the alignment in the domain type. This pins that it
// survives being rendered to protobuf — a conversion that goes through
// timestamppb and could plausibly be given an unaligned value by a future
// change to the schedule builder.
func TestTheAnnouncedTimetableIsSlotAlignedOnTheWire(t *testing.T) {
	// Deliberately ragged; NewFest aligns it.
	start := time.Date(2026, 9, 5, 13, 1, 35, 0, time.UTC)
	f := NewFest(start, nil)
	svc, _ := newService(t, f.Timetable().Start.Add(time.Hour))

	if err := svc.SaveFest(context.Background(), f, true, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	resp, err := svc.SelectFestSchedule(context.Background(),
		&toyohrpb.SelectFestScheduleRequest{Tenant: testTenant, Region: "JP"})
	if err != nil {
		t.Fatalf("SelectFestSchedule: %v", err)
	}

	tt := resp.GetSchedule().GetTimetable()
	for name, ts := range map[string]int64{
		"open":  tt.GetOpenTime().GetSeconds(),
		"start": tt.GetStartTime().GetSeconds(),
		"mid":   tt.GetMidTime().GetSeconds(),
		"end":   tt.GetEndTime().GetSeconds(),
		"close": tt.GetCloseTime().GetSeconds(),
	} {
		if ts%slotSeconds != 0 {
			t.Errorf("%s is at %d, %d seconds off a two-hour slot boundary",
				name, ts, ts%slotSeconds)
		}
	}
}

// Before its announcement, a configured fest is not announced.
func TestNoFestIsAnnouncedBeforeItsAnnouncement(t *testing.T) {
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	f := NewFest(start, nil)
	svc, _ := newService(t, f.Timetable().Open.Add(-48*time.Hour))
	if err := svc.SaveFest(context.Background(), f, true, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	resp, err := svc.SelectFestSchedule(context.Background(),
		&toyohrpb.SelectFestScheduleRequest{Tenant: testTenant, Region: "JP"})
	if err != nil {
		t.Fatalf("SelectFestSchedule: %v", err)
	}
	if resp.GetSchedule() != nil {
		t.Errorf("announced a fest long before its announcement")
	}
}

// With no fest live, the last closed one is announced, disabled or past its
// publication window: an empty schedule leaves the console's fest Invalid,
// and the lobby's big screens only exist on fest layers (2026-10-19,
// hardware: blank for everyone once the schedule went empty).
func TestTheLastClosedFestIsAnnouncedWhenNoFestIsLive(t *testing.T) {
	older := NewFest(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), nil)
	older.ID = "JUEA-90001"
	last := NewFest(time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), nil)
	last.ID = "JUEA-90002"
	_, end := last.PublishWindow()

	for _, enabled := range []bool{true, false} {
		svc, _ := newService(t, end.Add(time.Hour))
		for _, f := range []Fest{older, last} {
			if err := svc.SaveFest(context.Background(), f, enabled, nil); err != nil {
				t.Fatalf("save: %v", err)
			}
		}
		resp, err := svc.SelectFestSchedule(context.Background(),
			&toyohrpb.SelectFestScheduleRequest{Tenant: testTenant, Region: "JP"})
		if err != nil {
			t.Fatalf("SelectFestSchedule: %v", err)
		}
		if got := resp.GetSchedule().GetFest(); !strings.HasSuffix(got, "/fests/JUEA-90002") {
			t.Errorf("enabled=%v: announced %q, want the last closed fest JUEA-90002", enabled, got)
		}
	}
}

// A fest that is only announced or scheduled is never the fallback: the
// fallback is for a fest whose results are out.
func TestAnUpcomingDisabledFestIsNotAnnounced(t *testing.T) {
	f := NewFest(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), nil)
	svc, _ := newService(t, f.Timetable().Start)
	if err := svc.SaveFest(context.Background(), f, false, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	resp, err := svc.SelectFestSchedule(context.Background(),
		&toyohrpb.SelectFestScheduleRequest{Tenant: testTenant, Region: "JP"})
	if err != nil {
		t.Fatalf("SelectFestSchedule: %v", err)
	}
	if resp.GetSchedule() != nil {
		t.Errorf("announced a disabled fest that is still being played")
	}
}

// The results phase is announced: the game shows the results from this
// schedule, and dropping a closed fest hides them.
func TestAFestIsStillAnnouncedOnceItsResultsAreOut(t *testing.T) {
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	f := NewFest(start, nil)
	svc, _ := newService(t, f.Timetable().Close.Add(time.Hour))
	if err := svc.SaveFest(context.Background(), f, true, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	resp, err := svc.SelectFestSchedule(context.Background(),
		&toyohrpb.SelectFestScheduleRequest{Tenant: testTenant, Region: "JP"})
	if err != nil {
		t.Fatalf("SelectFestSchedule: %v", err)
	}
	if resp.GetSchedule() == nil {
		t.Fatal("a fest with published results was not announced")
	}
}

// TestAFestIsAnnouncedFromTheAnnouncementRightThroughToTheResults.
//
// "Current" is not the same as "playable": the fest appears in the lobby a week
// before it starts and its results stay up for two hours after play ends.
// Announcing only during play would make the fest vanish from the lobby the
// moment it ended, before anyone could see who won.
func TestAFestIsAnnouncedFromTheAnnouncementRightThroughToTheResults(t *testing.T) {
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	f := NewFest(start, nil)
	tt := f.Timetable()

	for name, at := range map[string]time.Time{
		"just after the announcement": tt.Open.Add(time.Minute),
		"first half":                  tt.Start.Add(time.Hour),
		"second half":                 tt.Mid.Add(time.Hour),
		"counting the results":        tt.End.Add(time.Minute),
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newService(t, at)
			if err := svc.SaveFest(context.Background(), f, true, nil); err != nil {
				t.Fatalf("save: %v", err)
			}
			resp, err := svc.SelectFestSchedule(context.Background(),
				&toyohrpb.SelectFestScheduleRequest{Tenant: testTenant, Region: "JP"})
			if err != nil {
				t.Fatalf("SelectFestSchedule: %v", err)
			}
			if resp.GetSchedule() == nil {
				t.Errorf("no fest announced %s", name)
			}
		})
	}
}

// TestADisabledFestIsNotAnnounced: the operator's disabled fest must not show.
func TestADisabledFestIsNotAnnounced(t *testing.T) {
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	svc, _ := newService(t, start.Add(6*time.Hour))

	f := NewFest(start, nil)
	if err := svc.SaveFest(context.Background(), f, false, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	resp, err := svc.SelectFestSchedule(context.Background(),
		&toyohrpb.SelectFestScheduleRequest{Tenant: testTenant, Region: "JP"})
	if err != nil {
		t.Fatalf("SelectFestSchedule: %v", err)
	}
	if resp.GetSchedule() != nil {
		t.Error("a fest the operator has not enabled was announced")
	}
}

// A JP-only fest must not reach an EU player.
func TestAFestIsNotAnnouncedToARegionItDoesNotRunIn(t *testing.T) {
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	svc, _ := newService(t, start.Add(6*time.Hour))

	f := NewFest(start, []string{"JP"})
	if err := svc.SaveFest(context.Background(), f, true, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	resp, err := svc.SelectFestSchedule(context.Background(),
		&toyohrpb.SelectFestScheduleRequest{Tenant: testTenant, Region: "EU"})
	if err != nil {
		t.Fatalf("SelectFestSchedule: %v", err)
	}
	if resp.GetSchedule() != nil {
		t.Error("a JP-only fest was announced to EU")
	}
}

// TestTheDecryptionKeysComeBackInTheOrderTheMessageDeclares.
//
// The keys are positional: notice, start, result, then the three per-team keys.
// A key in the wrong slot is handed to the console for the wrong phase, and the
// pack fails to open with nothing saying which phase was wrong.
func TestTheDecryptionKeysComeBackInTheOrderTheMessageDeclares(t *testing.T) {
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	svc, _ := newService(t, start.Add(6*time.Hour))

	f := NewFest(start, nil)
	keys := []string{
		"000102030405060708090a0b0c0d0e0f", // notice
		"101112131415161718191a1b1c1d1e1f", // start
		"202122232425262728292a2b2c2d2e2f", // result
	}
	if err := svc.SaveFest(context.Background(), f, true, keys); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := svc.GetFestDecryptionKey(context.Background(),
		&toyohrpb.GetFestDecryptionKeyRequest{
			Name: "tenants/" + testTenant + "/fests/" + f.ID + "/decryptionKey",
		})
	if err != nil {
		t.Fatalf("GetFestDecryptionKey: %v", err)
	}

	if got.GetNoticeKey() != keys[0] {
		t.Errorf("notice key = %q, want %q", got.GetNoticeKey(), keys[0])
	}
	if got.GetStartKey() != keys[1] {
		t.Errorf("start key = %q, want %q", got.GetStartKey(), keys[1])
	}
	if got.GetResultWinningTeamKey() != "" {
		t.Errorf("result key was published before the results opened: %q", got.GetResultWinningTeamKey())
	}
	svc.now = func() time.Time { return f.Timetable().Close.Add(time.Minute) }
	closed, err := svc.GetFestDecryptionKey(context.Background(),
		&toyohrpb.GetFestDecryptionKeyRequest{Name: f.ID})
	if err != nil || closed.GetResultWinningTeamKey() != keys[2] {
		t.Fatalf("closed fest result key = %q, error %v; want %q", closed.GetResultWinningTeamKey(), err, keys[2])
	}

	// A key that was not supplied is left ABSENT, not empty. Nintendo omits the
	// key for a phase that has not happened; an empty string is a key the
	// console would try to use.
	if got.GetTeamAlphaKey() != "" {
		t.Errorf("an unsupplied team key came back as %q, want absent", got.GetTeamAlphaKey())
	}
}

// Explicit show_all_keys requests can still retrieve stored optional slots.
func TestAllSixKeySlotsAreAvailable(t *testing.T) {
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	svc, _ := newService(t, start.Add(51*time.Hour))

	f := NewFest(start, nil)
	keys := []string{
		"00000000000000000000000000000000",
		"11111111111111111111111111111111",
		"22222222222222222222222222222222",
		"33333333333333333333333333333333",
		"44444444444444444444444444444444",
		"55555555555555555555555555555555",
	}
	if err := svc.SaveFest(context.Background(), f, true, keys); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := svc.GetFestDecryptionKey(context.Background(),
		&toyohrpb.GetFestDecryptionKeyRequest{Name: f.ID, ShowAllKeys: true})
	if err != nil {
		t.Fatalf("GetFestDecryptionKey: %v", err)
	}
	for i, want := range map[int]string{
		3: got.GetTeamAlphaKey(),
		4: got.GetTeamBravoKey(),
		5: got.GetTeamCharlieKey(),
	} {
		if want != keys[i] {
			t.Errorf("key slot %d = %q, want %q", i, want, keys[i])
		}
	}
}

func TestAnUnknownFestIsNotFound(t *testing.T) {
	svc, _ := newService(t, time.Now())
	_, err := svc.GetFestDecryptionKey(context.Background(),
		&toyohrpb.GetFestDecryptionKeyRequest{Name: "NXTD-99999"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("an unknown fest returned %v, want NotFound", err)
	}
}

// TestAFestThatWouldFailOnHardwareIsRefusedAtSaveTime.
//
// The two failures worth catching before a console sees them are a misaligned
// start and a capitalised resource name. Both produce a generic communication
// error on the console with nothing pointing at the cause, so the operator has
// to be told at the point they set it.
func TestAFestThatWouldFailOnHardwareIsRefusedAtSaveTime(t *testing.T) {
	svc, store := newService(t, time.Now())
	good := NewFest(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), nil)

	misaligned := good
	misaligned.Start = misaligned.Start.Add(90 * time.Second)
	if err := svc.SaveFest(context.Background(), misaligned, true, nil); err == nil {
		t.Error("a misaligned fest was saved")
	}

	capitalised := good
	capitalised.Resource = "NXTD-00042_Data"
	if err := svc.SaveFest(context.Background(), capitalised, true, nil); err == nil {
		t.Error("a fest with a capitalised resource name was saved")
	}

	shortKey := good
	if err := svc.SaveFest(context.Background(), shortKey, true, []string{"abcd"}); err == nil {
		t.Error("a pack key that is not a 16-byte AES key was saved")
	}

	// None of the rejected fests reached the store.
	fests, err := store.ListFests(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(fests) != 0 {
		t.Errorf("%d invalid fests were stored", len(fests))
	}
}

// TestAPackThatDoesNotExistKeepsItsKeySlotEmpty guards the positional contract
// between SaveFest and assignPackKeys.
//
// The keys are returned positionally — notice, start, result, then one per team
// — so a two-team fest, which has no wc pack, needs an empty slot rather than a
// shorter list. Requiring 32 characters everywhere would mean either inventing a
// key for a pack nobody built or shifting every later key by one, which hands
// the console the wrong key for the wrong phase and fails with nothing saying
// which phase was wrong.
//
// This was found by running `switchnetctl fest build` rather than by a test:
// SaveFest rejected exactly the shape the builder produces.
func TestAPackThatDoesNotExistKeepsItsKeySlotEmpty(t *testing.T) {
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	svc, store := newService(t, start.Add(-24*time.Hour))

	f := NewFest(start, DefaultRegions)
	key := strings.Repeat("ab", 16)
	// base and bh built; the three team packs are not.
	if err := svc.SaveFest(context.Background(), f, true,
		[]string{key, key, "", "", "", ""}); err != nil {
		t.Fatalf("a fest with unbuilt packs was refused: %v", err)
	}

	row, err := store.GetFest(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("get fest: %v", err)
	}
	if len(row.PackKeys) != 6 || row.PackKeys[2] != "" {
		t.Errorf("keys stored as %v, want the empty slots kept", row.PackKeys)
	}

	// A key that is present must still be the right length: a truncated key is
	// a pack that never opens, and that is worth refusing at the point it is
	// entered.
	if err := svc.SaveFest(context.Background(), f, true, []string{"abcd"}); err == nil {
		t.Error("a short pack key was accepted")
	}
}

// TestATeamPackKeyReachesItsOwnTeamField is the other half of
// bcat.TestEveryPackHasExactlyOneKeyField: the mapping is right in bcat, and it
// survives the trip through the store and out of GetFestDecryptionKey.
func TestATeamPackKeyReachesItsOwnTeamField(t *testing.T) {
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	svc, _ := newService(t, start.Add(51*time.Hour))

	f := NewFest(start, DefaultRegions)
	keys := bcat.KeysInFieldOrder(map[string][]byte{
		bcat.PackBase:      bytes.Repeat([]byte{0xaa}, 16),
		bcat.PackTeamAlpha: bytes.Repeat([]byte{0xbb}, 16),
		bcat.PackTeamBravo: bytes.Repeat([]byte{0xcc}, 16),
	})
	if err := svc.SaveFest(context.Background(), f, true, keys); err != nil {
		t.Fatalf("save fest: %v", err)
	}

	got, err := svc.GetFestDecryptionKey(context.Background(),
		&toyohrpb.GetFestDecryptionKeyRequest{
			Name:        "tenants/" + testTenant + "/fests/" + f.ID + "/decryptionKey",
			ShowAllKeys: true,
		})
	if err != nil {
		t.Fatalf("get keys: %v", err)
	}

	if got.GetTeamAlphaKey() != strings.Repeat("bb", 16) {
		t.Errorf("team_alpha_key = %q, want wa's key", got.GetTeamAlphaKey())
	}
	if got.GetTeamBravoKey() != strings.Repeat("cc", 16) {
		t.Errorf("team_bravo_key = %q, want wb's key", got.GetTeamBravoKey())
	}
	// wc was not built, so its field stays absent rather than carrying wb's key
	// shifted along — which is what a positional five-onto-six list did.
	if got.GetTeamCharlieKey() != "" {
		t.Errorf("team_charlie_key = %q, but no wc pack exists", got.GetTeamCharlieKey())
	}
	if got.GetResultWinningTeamKey() != "" {
		t.Errorf("result_winning_team_key = %q, but no pack corresponds to it",
			got.GetResultWinningTeamKey())
	}
}

func TestOrdinaryFestKeyResponsesOmitFuturePackFieldsOnTheWire(t *testing.T) {
	start := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{-24 * time.Hour, time.Hour, 25 * time.Hour} {
		t.Run(offset.String(), func(t *testing.T) {
			svc, _ := newService(t, start.Add(offset))
			f := NewFest(start, DefaultRegions)
			keys := []string{strings.Repeat("11", 16), strings.Repeat("22", 16), strings.Repeat("33", 16), strings.Repeat("44", 16), strings.Repeat("55", 16), strings.Repeat("66", 16)}
			if err := svc.SaveFest(context.Background(), f, true, keys); err != nil {
				t.Fatal(err)
			}
			for _, all := range []bool{false, true} {
				got, err := svc.GetFestDecryptionKey(context.Background(), &toyohrpb.GetFestDecryptionKeyRequest{Name: f.ID, ShowAllKeys: all})
				if err != nil {
					t.Fatal(err)
				}
				raw, err := proto.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				seen := map[protowire.Number]bool{}
				for len(raw) > 0 {
					field, kind, n := protowire.ConsumeTag(raw)
					if n < 0 {
						t.Fatal("invalid tag")
					}
					raw = raw[n:]
					if field < 1 || field > 3 {
						t.Fatalf("active fest published unexpected key field %d", field)
					}
					_, n = protowire.ConsumeBytes(raw)
					if kind != protowire.BytesType || n < 0 {
						t.Fatal("invalid key field")
					}
					raw = raw[n:]
					seen[field] = true
				}
				if len(seen) != 3 {
					t.Fatalf("wire fields=%v, want name, notice and start", seen)
				}
			}
		})
	}
}

// The console asks for the results of the closed fest it is shown; a
// disabled fest it can be shown must answer, not NotFound.
func TestADisabledClosedFestStillAnswersItsResults(t *testing.T) {
	f := NewFest(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), nil)
	_, end := f.PublishWindow()
	svc, _ := newService(t, end.Add(time.Hour))
	if err := svc.SaveFest(context.Background(), f, false, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := svc.GetFestResult(context.Background(), &toyohrpb.GetFestResultRequest{
		Name: "tenants/" + testTenant + "/fests/" + f.ID + "/result",
	}); err != nil {
		t.Errorf("GetFestResult for a disabled closed fest: %v", err)
	}
}
