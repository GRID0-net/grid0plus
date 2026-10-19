package ugcstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
)

// ApplyCoopGrades runs before the final Gamesync verdict is published. The
// client refreshes CoopUserAttribute, then displays its total_grade_point
// (v11.3.0: 0x37dc070 and 0x37dcb70); replaying the kickoff's unchanged
// challenge_grade_point cannot advance the player's grade. Missing slots in
// a deliberately small lobby are not disconnected players.
func ApplyCoopGrades(ctx context.Context, store db.Store, tenant, battleID, mode string,
	verdict *commonpb.MapValue, users map[string]string, now time.Time) error {
	if strings.Contains(mode, "private") || strings.Contains(mode, "team_contest") || battleID == "" || strings.Contains(battleID, "/") {
		return nil
	}
	f := verdict.GetFields()
	team := f["team_result"].GetMapValue().GetFields()
	result, wave := team["result"], team["final_wave"]
	if result == nil || wave == nil {
		return nil
	}
	// Observed results: result=0 always with final_wave=2 (a clear; final_wave
	// counts from 0), result=1 with final_wave 0, 1 or 2 (failed in that wave).
	// UNCONFIRMED: any other result value, a disconnect or a King Salmonid wave, has
	// never been seen. Unknown results must not award or deduct grade. Change
	// ApplyCoopGrades.
	if result.GetIntegerValue() < 0 || result.GetIntegerValue() > 1 || wave.GetIntegerValue() < 0 || wave.GetIntegerValue() > 3 {
		return nil
	}
	if result.GetIntegerValue() == 0 && wave.GetIntegerValue() < 2 {
		return nil
	}
	encounter := kingSalmonidEncountered(f["team_result"])
	people := f["personal_result"].GetMapValue().GetFields()
	disconnect := false
	for _, v := range people {
		e := v.GetMapValue().GetFields()
		disconnect = disconnect || e["disconnected"].GetBooleanValue()
		// The first reporter may not have its peers' personal_result entries
		// yet. Its report_score already records peer disconnects by user ID.
		for _, score := range e["report_score"].GetMapValue().GetFields() {
			disconnect = disconnect || score.GetMapValue().GetFields()["disconnect"].GetIntegerValue() != 0
		}
	}
	for key, value := range people {
		entry := value.GetMapValue().GetFields()
		if entry == nil {
			continue
		}
		uid := users[key]
		if uid == "" {
			uid = entry["npln_user_id"].GetStringValue()
		}
		if uid == "" || strings.Contains(uid, "/") {
			continue
		}
		delta := int64(20)
		if result.GetIntegerValue() != 0 {
			delta = -20 + min(wave.GetIntegerValue(), 2)*10
			if disconnect && !entry["disconnected"].GetBooleanValue() {
				delta /= 2
			}
		}
		if entry["is_supporter"].GetBooleanValue() && delta > 0 {
			delta = 0
		}
		grades, err := creditCoopGrade(ctx, store, tenant, battleID, uid, delta, &encounter, now)
		if err != nil {
			return err
		}
		for k, v := range grades.GetFields() {
			entry[k] = v
		}
	}
	return nil
}

// The total coordinate uses 100-point title bands below 800, then the
// Eggsecutive VP 0..999 range at 800..1799. The game converts that coordinate
// at 0x38ca558..0x38ca58c; these are not per-title gauge points.
func nextCoopGrade(before, delta int64) int64 {
	after := max(int64(0), min(int64(1799), before+delta))
	oldTitle, newTitle := min(before/100, int64(8)), min(after/100, int64(8))
	if newTitle != oldTitle {
		after = newTitle*100 + 40
	}
	return after
}

// smellMax is a full Salmometer. The game sums every player's smell into the
// job's scenario.smell_num (from each member's copy of CoopUserAttribute.smell)
// and rolls the King Salmonid at the end of wave 3 from that sum; Inkipedia's
// datamine notes give the odds (0 up to a sum of 4, certain at 20 = four full
// meters).
const smellMax = 5

// nextSmell is one player's Salmometer after a job: up one per job without
// a King Salmonid, whatever the result, and empty again after one.
// Nintendo's server keeps it; the console never reports a new value, only
// the sum it started the job with.
func nextSmell(before int64, encounter bool) int64 {
	if encounter {
		return 0
	}
	return min(max(before, 0)+1, smellMax)
}

// kingSalmonidEncountered reports an Xtrawave in a job's team result: a
// fourth wave (final_wave counts from 0, a cleared normal job ends at 2), or
// the game's own boss flags.
func kingSalmonidEncountered(team *commonpb.Value) bool {
	return firstNamed(team, "final_wave") >= 3 || firstNamed(team, "spawned_boss") != 0 ||
		firstNamed(team, "did_defeat_boss") != 0
}

// creditCoopGrade moves one player's grade once per job: the marker, written
// in the same transaction, makes a retried report a no-op. A nil encounter
// leaves the Salmometer alone -- a LAN job is not played against GRID0+'s
// copy of it. Every Eggsecutive VP result also raises the shift's best, which
// is what the stage's badge is read from (CoopBadgeReset).
func creditCoopGrade(ctx context.Context, store db.Store, tenant, battle, uid string, delta int64, encounter *bool, now time.Time) (*commonpb.MapValue, error) {
	root := "tenants/" + tenant + documentsSegment
	marker := root + "grid0/coopGradeCredits/" + battle + "_" + uid
	name := root + "services/GameRecord/users/" + uid + "/CoopUserAttribute/Data"
	for attempt := 0; attempt < 4; attempt++ {
		if d, err := store.GetUGCDocument(ctx, marker); err == nil {
			return decodeFields(d.Fields)
		} else if !errors.Is(err, db.ErrNotFound) {
			return nil, err
		}
		d, err := store.GetUGCDocument(ctx, name)
		var previous *time.Time
		if errors.Is(err, db.ErrNotFound) {
			d = db.UGCDocument{Name: name, Owner: uid, CreateTime: now}
		} else if err != nil {
			return nil, err
		} else {
			t := d.UpdateTime
			previous = &t
		}
		attrs, err := decodeFields(d.Fields)
		if err != nil {
			return nil, err
		}
		before := attrs.Fields["total_grade_point"].GetIntegerValue()
		if before < 0 || before > 1799 {
			return nil, fmt.Errorf("invalid stored Coop grade coordinate")
		}
		after := nextCoopGrade(before, delta)
		grades := &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"grade_point_before": integer(before), "grade_point_after": integer(after),
			"grade_before": integer(min(before/100, 8)), "grade_after": integer(min(after/100, 8)),
		}}
		if encounter != nil {
			smellBefore := attrs.Fields["smell"].GetIntegerValue()
			smellAfter := nextSmell(smellBefore, *encounter)
			grades.Fields["smell_before"], grades.Fields["smell_after"] = integer(smellBefore), integer(smellAfter)
			attrs.Fields["smell"] = integer(smellAfter)
		}
		attrs.Fields["total_grade_point"] = integer(after)
		attrs.Fields["grade"] = integer(min(after/100, 8))
		d.Fields, err = encodeFields(attrs)
		if err != nil {
			return nil, err
		}
		d.UpdateTime = now
		encoded, err := encodeFields(grades)
		if err != nil {
			return nil, err
		}
		changes := []db.UGCDocumentCAS{
			{Document: d, PreviousUpdateTime: previous},
			{Document: db.UGCDocument{Name: marker, Owner: uid, Fields: encoded, CreateTime: now, UpdateTime: now}},
		}
		if shift := attrs.Fields["shift_id"].GetStringValue(); after >= evpBase && shift != "" {
			best, err := raiseCoopBest(ctx, store, root, uid, shift, after-evpBase, now)
			if err != nil {
				return nil, err
			}
			if best != nil {
				changes = append(changes, *best)
			}
		}
		err = store.CompareAndPutUGCDocuments(ctx, changes)
		if errors.Is(err, db.ErrConflict) {
			continue
		}
		return grades, err
	}
	return nil, db.ErrConflict
}
