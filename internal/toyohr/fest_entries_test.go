package toyohr

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/n-popescu/switchnet/internal/rpcauth"
	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// callerCtx returns a context carrying an authenticated caller, the way
// internal/npln's auth interceptor would set it up before dispatching to
// this service -- these tests call the Service directly, with no
// interceptor in front of it, so the context has to be built by hand.
func callerCtx(userID string) context.Context {
	return rpcauth.With(context.Background(), rpcauth.Caller{UserID: userID})
}

func mustSaveFest(t *testing.T, svc *Service, id string) Fest {
	t.Helper()
	f := NewFest(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), nil)
	f.ID = id
	f.Revision = NewRevision(f.ID, f.Resource)
	if err := svc.SaveFest(context.Background(), f, true, nil); err != nil {
		t.Fatalf("save fest: %v", err)
	}
	return f
}

func TestCreateFestEntryRecordsAndReturnsTheCallersChoice(t *testing.T) {
	svc, _ := newService(t, time.Now())
	f := mustSaveFest(t, svc, "NXTD-00042")

	got, err := svc.CreateFestEntry(callerCtx("alice000000000a"),
		&toyohrpb.CreateFestEntryRequest{
			Parent: "tenants/" + testTenant + "/fests/" + f.ID,
			FestEntry: &toyohrpb.FestEntry{
				FestTeam: "wa", FestRegion: "jp",
			},
		})
	if err != nil {
		t.Fatalf("CreateFestEntry: %v", err)
	}
	if got.GetFestTeam() != "wa" {
		t.Errorf("fest_team = %q, want wa", got.GetFestTeam())
	}
	if got.GetFestRegion() != "JP" {
		t.Errorf("fest_region = %q, want it uppercased to JP", got.GetFestRegion())
	}
	wantName := "tenants/" + testTenant + "/fests/NXTD-00042/entries/alice000000000a"
	if got.GetName() != wantName {
		t.Errorf("name = %q, want %q", got.GetName(), wantName)
	}

	// The caller can read back their own entry.
	read, err := svc.GetFestEntry(callerCtx("alice000000000a"),
		&toyohrpb.GetFestEntryRequest{Name: wantName})
	if err != nil {
		t.Fatalf("GetFestEntry: %v", err)
	}
	if read.GetFestTeam() != "wa" {
		t.Errorf("read back fest_team = %q, want wa", read.GetFestTeam())
	}
}

// TestATeamChoiceIsPermanent covers the Splatfest rule Create/GetFestEntry
// are built around: once picked, a team cannot be changed, so a second
// CreateFestEntry for the same fest and caller must not silently overwrite
// the first.
func TestATeamChoiceIsPermanent(t *testing.T) {
	svc, _ := newService(t, time.Now())
	f := mustSaveFest(t, svc, "NXTD-00042")
	parent := "tenants/" + testTenant + "/fests/" + f.ID

	if _, err := svc.CreateFestEntry(callerCtx("alice000000000a"),
		&toyohrpb.CreateFestEntryRequest{
			Parent: parent, FestEntry: &toyohrpb.FestEntry{FestTeam: "wa"},
		}); err != nil {
		t.Fatalf("first CreateFestEntry: %v", err)
	}

	_, err := svc.CreateFestEntry(callerCtx("alice000000000a"),
		&toyohrpb.CreateFestEntryRequest{
			Parent: parent, FestEntry: &toyohrpb.FestEntry{FestTeam: "wb"},
		})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("second CreateFestEntry (different team) = %v, want AlreadyExists", err)
	}

	// The original choice is unchanged.
	got, err := svc.GetFestEntry(callerCtx("alice000000000a"),
		&toyohrpb.GetFestEntryRequest{Name: parent + "/entries/alice000000000a"})
	if err != nil {
		t.Fatalf("GetFestEntry: %v", err)
	}
	if got.GetFestTeam() != "wa" {
		t.Errorf("fest_team = %q after a rejected re-entry, want it to stay wa", got.GetFestTeam())
	}
}

// TestGetFestEntryIsSelfOnly guards the same "cannot read someone else's
// data" boundary internal/baas's requireSelf enforces on every user-scoped
// endpoint -- a team pick should not be readable by another account before
// results are public.
func TestGetFestEntryIsSelfOnly(t *testing.T) {
	svc, _ := newService(t, time.Now())
	f := mustSaveFest(t, svc, "NXTD-00042")

	if _, err := svc.CreateFestEntry(callerCtx("alice000000000a"),
		&toyohrpb.CreateFestEntryRequest{
			Parent:    "tenants/" + testTenant + "/fests/" + f.ID,
			FestEntry: &toyohrpb.FestEntry{FestTeam: "wa"},
		}); err != nil {
		t.Fatalf("CreateFestEntry: %v", err)
	}

	_, err := svc.GetFestEntry(callerCtx("bob0000000000b1"),
		&toyohrpb.GetFestEntryRequest{
			Name: "tenants/" + testTenant + "/fests/" + f.ID + "/entries/alice000000000a",
		})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("a different caller reading alice's entry = %v, want PermissionDenied", err)
	}
}

func TestCreateFestEntryRequiresAnExistingFest(t *testing.T) {
	svc, _ := newService(t, time.Now())

	_, err := svc.CreateFestEntry(callerCtx("alice000000000a"),
		&toyohrpb.CreateFestEntryRequest{
			Parent:    "tenants/" + testTenant + "/fests/NXTD-99999",
			FestEntry: &toyohrpb.FestEntry{FestTeam: "wa"},
		})
	if status.Code(err) != codes.NotFound {
		t.Errorf("entering an unknown fest = %v, want NotFound", err)
	}
}

func TestCreateFestEntryRequiresAFestTeam(t *testing.T) {
	svc, _ := newService(t, time.Now())
	f := mustSaveFest(t, svc, "NXTD-00042")

	_, err := svc.CreateFestEntry(callerCtx("alice000000000a"),
		&toyohrpb.CreateFestEntryRequest{
			Parent:    "tenants/" + testTenant + "/fests/" + f.ID,
			FestEntry: &toyohrpb.FestEntry{},
		})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("no fest_team = %v, want InvalidArgument", err)
	}
}

// TestGetFestResultReportsNotYetValid is the honest-emptiness case: SwitchNet
// has no real battle-outcome data to compute a ratio or point total from, so
// every sub-result says IsValid: false rather than a fabricated number. See
// GetFestResult's own TODO(capture-needed) for why.
func TestGetFestResultReportsNotYetValid(t *testing.T) {
	svc, _ := newService(t, time.Now())
	f := mustSaveFest(t, svc, "NXTD-00042")

	got, err := svc.GetFestResult(context.Background(),
		&toyohrpb.GetFestResultRequest{Name: "tenants/" + testTenant + "/fests/" + f.ID + "/result"})
	if err != nil {
		t.Fatalf("GetFestResult: %v", err)
	}
	if got.GetYobisai().GetIsValid() {
		t.Error("yobisai reported valid with no battle data to compute it from")
	}
	if got.GetHonsaiMidterm().GetIsValid() {
		t.Error("honsai_midterm reported valid with no battle data to compute it from")
	}
	if got.GetHonsaiFinal().GetIsValid() {
		t.Error("honsai_final reported valid with no battle data to compute it from")
	}
	if got.GetOverall().GetIsValid() {
		t.Error("overall reported valid with no battle data to compute it from")
	}
}

func TestGetFestResultOfAnUnknownFestIsNotFound(t *testing.T) {
	svc, _ := newService(t, time.Now())
	_, err := svc.GetFestResult(context.Background(),
		&toyohrpb.GetFestResultRequest{Name: "tenants/" + testTenant + "/fests/NXTD-99999/result"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("result of an unknown fest = %v, want NotFound", err)
	}
}
