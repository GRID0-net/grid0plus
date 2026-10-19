package toyohr

import (
	"context"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/proto/common"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

func festSave(festID string, team *common.Value) *toyohrpb.SaveRecord {
	fest := map[string]*common.Value{"FestId": {ValueType: &common.Value_StringValue{StringValue: festID}}}
	if team != nil {
		fest["VoteTeam"] = team
	}
	return &toyohrpb.SaveRecord{SaveData: &common.MapValue{Fields: map[string]*common.Value{
		"Fest": {ValueType: &common.Value_MapValue{MapValue: &common.MapValue{Fields: fest}}},
	}}}
}

func savedTeam(t *testing.T, svc *Service) *common.Value {
	t.Helper()
	rec, err := svc.GetSaveRecord(context.Background(), &toyohrpb.GetSaveRecordRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return rec.GetSaveData().GetFields()["Fest"].GetMapValue().GetFields()["VoteTeam"]
}

// A team picked outside the game (on Discord) reaches the game only through
// the save: on hardware the game ignored the entry, asked the player to pick,
// and froze on GetFestEntry's answer; with VoteTeam set it took the team.
func TestASaveCarriesTheTeamOfThePlayersFestEntry(t *testing.T) {
	const uid = "aaaaaaaaaaaaaaaa"
	as(t, uid)
	svc, store := newService(t, time.Now())
	ctx := context.Background()
	if err := store.UpsertFest(ctx, db.Fest{ID: "JUEA-90004", StartAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateFestEntry(ctx, db.FestEntry{FestID: "JUEA-90004", UserID: uid, Team: "Bravo"}); err != nil {
		t.Fatal(err)
	}

	// The game's InitFest write: this fest, no team yet.
	if _, err := svc.WriteSaveRecord(ctx, &toyohrpb.WriteSaveRecordRequest{SaveRecord: festSave("JUEA-90004", intValue(-1))}); err != nil {
		t.Fatal(err)
	}
	if got := savedTeam(t, svc); got.GetIntegerValue() != 1 {
		t.Fatalf("VoteTeam = %v, want 1 (Bravo, the entry's team)", got)
	}
	// A later write naming another team does not change it: the first pick
	// is final.
	if _, err := svc.WriteSaveRecord(ctx, &toyohrpb.WriteSaveRecordRequest{SaveRecord: festSave("JUEA-90004", intValue(0))}); err != nil {
		t.Fatal(err)
	}
	if got := savedTeam(t, svc); got.GetIntegerValue() != 1 {
		t.Errorf("after a write naming Alpha, VoteTeam = %v, want 1", got)
	}
}

// A save not yet on the fest is left alone: its Fest map is the game's to
// start, and building one would invent its shape.
func TestASaveOnAnotherFestKeepsItsTeam(t *testing.T) {
	const uid = "aaaaaaaaaaaaaaaa"
	as(t, uid)
	svc, store := newService(t, time.Now())
	ctx := context.Background()
	if err := store.UpsertFest(ctx, db.Fest{ID: "JUEA-90004", StartAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateFestEntry(ctx, db.FestEntry{FestID: "JUEA-90004", UserID: uid, Team: "Bravo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WriteSaveRecord(ctx, &toyohrpb.WriteSaveRecordRequest{SaveRecord: festSave("JUEA-90003", intValue(2))}); err != nil {
		t.Fatal(err)
	}
	if got := savedTeam(t, svc); got.GetIntegerValue() != 2 {
		t.Errorf("an old fest's VoteTeam = %v, want 2 untouched", got)
	}
}

// A save left on a deleted fest waits for that fest's results forever; on
// grid0 two saves still named the deleted test fest JUEA-90004 two days later.
func TestASaveOnADeletedFestLosesItsFestBlock(t *testing.T) {
	const uid = "aaaaaaaaaaaaaaaa"
	as(t, uid)
	svc, store := newService(t, time.Now())
	ctx := context.Background()
	if err := store.UpsertFest(ctx, db.Fest{ID: "JUEA-90004", StartAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WriteSaveRecord(ctx, &toyohrpb.WriteSaveRecordRequest{SaveRecord: festSave("JUEA-90004", intValue(1))}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteFest(ctx, "JUEA-90004"); err != nil {
		t.Fatal(err)
	}

	rec, err := svc.GetSaveRecord(ctx, &toyohrpb.GetSaveRecordRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.GetSaveData().GetFields()["Fest"]; ok {
		t.Errorf("a save read after its fest was deleted still has a Fest block")
	}
	// The running game still holds the old block and writes it back.
	if _, err := svc.WriteSaveRecord(ctx, &toyohrpb.WriteSaveRecordRequest{SaveRecord: festSave("JUEA-90004", intValue(1))}); err != nil {
		t.Fatal(err)
	}
	stored, err := svc.loadSave(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stored.GetSaveData().GetFields()["Fest"]; ok {
		t.Errorf("a write naming the deleted fest stored its Fest block")
	}
}

// A fest that never existed on this server is not a deleted one: imported
// saves carry Nintendo's own fest ids.
func TestASaveOnAFestThisServerNeverHadKeepsItsFestBlock(t *testing.T) {
	const uid = "aaaaaaaaaaaaaaaa"
	as(t, uid)
	svc, _ := newService(t, time.Now())
	ctx := context.Background()
	if _, err := svc.WriteSaveRecord(ctx, &toyohrpb.WriteSaveRecordRequest{SaveRecord: festSave("JUEA-00018", intValue(0))}); err != nil {
		t.Fatal(err)
	}
	if got := savedTeam(t, svc); got == nil {
		t.Errorf("an imported fest's block was dropped")
	}
}
