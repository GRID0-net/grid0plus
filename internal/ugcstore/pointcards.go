package ugcstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
)

// Salmon Run's point card -- the in-game Bonus Meter and Grizzco points --
// lives in two documents per player that the console only ever reads:
//
//	services/GameRecord/users/<uid>/PointCardTotal/Data
//	services/GameRecord/users/<uid>/PointCardRegular/<shift_id>
//
// The server keeps them from each finished job. Without this the meter stays
// at zero whatever the player does. Everything below, the field sets, the
// version and the points rule, is NextendoNetwork/splatoon-3's reading of
// its captures of Nintendo's server (facts only, not its code):
//
//   - PointCardTotal/Data has eight fields: job_num, golden_ikura_total,
//     ikura_total, rescue_total, boss_total, kuma_point, limited_kuma_point
//     and version 131072.
//   - PointCardRegular/<shift_id> adds the shift's grade, total and best grade
//     points, best golden eggs, total cleared waves, job_type_string,
//     shift_id and shift_time.
//   - kuma_point never comes from the console. Fitted over seventeen captured
//     jobs: 2 per golden egg the team delivered, plus 50 when the job is
//     cleared (final_wave 2), the same for all four players.
//
// TODO(capture-needed): the exact kuma_point rule. The fit is off by six
// points on average; a capture with more jobs would settle it. Change
// jobPoints.
const pointCardVersion = 131072

// shiftCard names the per-shift point card a job is credited to. Splatoon 3
// builds three document names under services/GameRecord/users/<uid>/ (the
// strings at 0x7104f46d02, 0x7104f50845 and 0x7104fe73c8): PointCardRegular,
// PointCardBigRun and PointCardLimited (Eggstra Work), each keyed by the
// shift's start as YYYYMMDDHH0000. For the Big Run and Eggstra Work cards
// the game reads best_golden_ikura_num and trophy together and ignores the
// card unless both are there (FUN_7103281354): the console writes trophy
// itself at the end of the event (FUN_7103286ae0, FUN_7103287030), so the
// server supplies best_golden_ikura_num.
func shiftCard(mode string) string {
	switch {
	case strings.Contains(mode, "bigrun"):
		return "PointCardBigRun"
	case strings.Contains(mode, "team_contest"):
		return "PointCardLimited"
	}
	return "PointCardRegular"
}

func jobPoints(deliveredGolden, finalWave int64) int64 {
	p := 2 * deliveredGolden
	if finalWave >= 2 {
		p += 50
	}
	return p
}

// RecordCoopPointCards credits one finished Salmon Run job to each player's
// point cards, once per player per job however many consoles report it.
// verdict is the published referee result; sessionUsers maps the user
// sessions personal_result is keyed by to their users.
func RecordCoopPointCards(ctx context.Context, store db.Store, tenant, battleID, mode string,
	verdict *commonpb.MapValue, sessionUsers map[string]string, now time.Time) error {
	f := verdict.GetFields()
	personal := f["personal_result"].GetMapValue()
	if battleID == "" || strings.Contains(battleID, "/") || personal == nil {
		return nil
	}
	team := f["team_result"]
	finalWave := firstNamed(team, "final_wave")
	if finalWave == 0 {
		finalWave = firstNamed(team, "total_clear_wave")
	}
	gradePoint := firstNamed(team, "total_grade_point")
	if gradePoint == 0 {
		gradePoint = f["challenge_grade_point"].GetIntegerValue()
	}
	delivered := sumNamed(team, "delivered_golden_ikura_num")
	shared := job{
		card:       shiftCard(mode),
		teamGolden: delivered,
		points:     jobPoints(delivered, finalWave),
		finalWave:  finalWave,
		gradePoint: gradePoint,
		grade:      firstNamed(team, "grade"),
		boss:       firstNamed(team, "did_defeat_boss"),
		shiftID:    f["shift_id"].GetStringValue(),
		shiftTime:  f["shift_time"].GetStringValue(),
		jobType:    f["job_type_string"].GetStringValue(),
	}
	// A shift_id can arrive as its whole range; the card is filed under its start.
	if i := strings.Index(shared.shiftID, "-"); i > 0 {
		shared.shiftID = shared.shiftID[:i]
	}
	if shared.shiftID == "" {
		if i := strings.Index(shared.shiftTime, "-"); i > 0 {
			shared.shiftID = shared.shiftTime[:i]
		}
	}

	root := "tenants/" + tenant + documentsSegment
	if shared.card == "PointCardBigRun" {
		if err := AddBigRunEggs(ctx, store, tenant, battleID, shared.shiftID, delivered, now); err != nil {
			return err
		}
	}
	for key, entry := range personal.GetFields() {
		uid := sessionUsers[key]
		if uid == "" {
			uid = entry.GetMapValue().GetFields()["npln_user_id"].GetStringValue()
		}
		if uid == "" || strings.Contains(uid, "/") {
			continue
		}
		// The marker lives outside services/GameRecord, so no query the
		// console makes can return it.
		marker := root + "grid0/coopCredits/" + battleID + "_" + uid
		if _, err := store.GetUGCDocument(ctx, marker); err == nil {
			continue
		} else if !errors.Is(err, db.ErrNotFound) {
			return err
		}
		j := shared
		if after := entry.GetMapValue().GetFields()["grade_point_after"]; after != nil {
			j.gradePoint = after.GetIntegerValue()
			j.grade = entry.GetMapValue().GetFields()["grade_after"].GetIntegerValue()
		}
		j.golden = sumNamed(entry, "golden_ikura_num")
		j.eggs = sumNamed(entry, "ikura_num")
		j.rescues = sumNamed(entry, "rescue_num")
		if err := creditPointCards(ctx, store, root+"services/GameRecord/users/"+uid+"/", uid, j, now); err != nil {
			return err
		}
		if err := store.PutUGCDocument(ctx, db.UGCDocument{
			Name: marker, Owner: uid, Fields: []byte{}, CreateTime: now, UpdateTime: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

type job struct {
	// card is the per-shift document this job goes on; see shiftCard.
	card                                       string
	teamGolden                                 int64
	points, finalWave, gradePoint, grade, boss int64
	golden, eggs, rescues                      int64
	shiftID, shiftTime, jobType                string
}

func creditPointCards(ctx context.Context, store db.Store, prefix, uid string, j job, now time.Time) error {
	total, err := loadCard(ctx, store, prefix+"PointCardTotal/Data")
	if err != nil {
		return err
	}
	add := func(m map[string]*commonpb.Value, k string, n int64) { m[k] = integer(m[k].GetIntegerValue() + n) }
	add(total, "job_num", 1)
	add(total, "golden_ikura_total", j.golden)
	add(total, "ikura_total", j.eggs)
	add(total, "rescue_total", j.rescues)
	add(total, "boss_total", j.boss)
	add(total, "kuma_point", j.points)
	add(total, "limited_kuma_point", 0)
	total["version"] = integer(pointCardVersion)
	if err := saveCard(ctx, store, prefix+"PointCardTotal/Data", uid, total, now); err != nil {
		return err
	}

	// Without a shift there is no name the game would look the card up by.
	if j.shiftID == "" {
		return nil
	}
	if err := creditShiftCard(ctx, store, prefix, uid, j.card, j, now); err != nil {
		return err
	}
	// The bonus meter is read from PointCardRegular/<shift> even during a Big
	// Run: FUN_7103284090 builds only the Regular or the Limited name, and the
	// console asks for PointCardRegular/<big run shift> after every Big Run
	// job. Without it the meter, and the catalog experience that follows it,
	// do not move.
	if j.card == "PointCardBigRun" {
		return creditShiftCard(ctx, store, prefix, uid, "PointCardRegular", j, now)
	}
	return nil
}

func creditShiftCard(ctx context.Context, store db.Store, prefix, uid, card string, j job, now time.Time) error {
	add := func(m map[string]*commonpb.Value, k string, n int64) { m[k] = integer(m[k].GetIntegerValue() + n) }
	name := prefix + card + "/" + j.shiftID
	shift, err := loadCard(ctx, store, name)
	if err != nil {
		return err
	}
	add(shift, "job_num", 1)
	add(shift, "golden_ikura_total", j.golden)
	add(shift, "ikura_total", j.eggs)
	add(shift, "rescue_total", j.rescues)
	add(shift, "boss_total", j.boss)
	add(shift, "kuma_point", j.points)
	add(shift, "total_clear_wave", j.finalWave)
	shift["total_grade_point"] = integer(j.gradePoint)
	shift["grade"] = integer(j.grade)
	if j.gradePoint > shift["best_grade_point"].GetIntegerValue() {
		shift["best_grade_point"] = integer(j.gradePoint)
	} else if shift["best_grade_point"] == nil {
		shift["best_grade_point"] = integer(0)
	}
	best := j.golden
	if card != "PointCardRegular" {
		// A Big Run or Eggstra Work high score is the team's golden eggs in
		// one job; the game compares it with the shift's reward borders.
		best = j.teamGolden
	}
	if best > shift["best_golden_ikura_num"].GetIntegerValue() {
		shift["best_golden_ikura_num"] = integer(best)
	} else if shift["best_golden_ikura_num"] == nil {
		shift["best_golden_ikura_num"] = integer(0)
	}
	shift["shift_id"] = str(j.shiftID)
	if j.shiftTime != "" {
		shift["shift_time"] = str(j.shiftTime)
	}
	if j.jobType != "" {
		shift["job_type_string"] = str(j.jobType)
	}
	shift["version"] = integer(pointCardVersion)
	return saveCard(ctx, store, name, uid, shift, now)
}

func loadCard(ctx context.Context, store db.Store, name string) (map[string]*commonpb.Value, error) {
	d, err := store.GetUGCDocument(ctx, name)
	if errors.Is(err, db.ErrNotFound) {
		return map[string]*commonpb.Value{}, nil
	}
	if err != nil {
		return nil, err
	}
	m, err := decodeFields(d.Fields)
	if err != nil {
		return nil, err
	}
	if m.Fields == nil {
		m.Fields = map[string]*commonpb.Value{}
	}
	return m.Fields, nil
}

func saveCard(ctx context.Context, store db.Store, name, uid string, fields map[string]*commonpb.Value, now time.Time) error {
	encoded, err := encodeFields(&commonpb.MapValue{Fields: fields})
	if err != nil {
		return err
	}
	created := now
	if d, err := store.GetUGCDocument(ctx, name); err == nil {
		created = d.CreateTime
	}
	return store.PutUGCDocument(ctx, db.UGCDocument{
		Name: name, Owner: uid, Fields: encoded, CreateTime: created, UpdateTime: now,
	})
}

// sumNamed adds every integer field called name at any depth: a player's
// counters sit in per-wave sub-messages whose nesting no capture pins down.
func sumNamed(v *commonpb.Value, name string) int64 {
	var total int64
	for k, sv := range v.GetMapValue().GetFields() {
		if k == name {
			total += sv.GetIntegerValue() + int64(sv.GetDoubleValue())
			continue
		}
		total += sumNamed(sv, name)
	}
	for _, e := range v.GetArrayValue().GetValues() {
		total += sumNamed(e, name)
	}
	return total
}

// firstNamed returns the first nonzero integer field called name, at any depth.
func firstNamed(v *commonpb.Value, name string) int64 {
	fields := v.GetMapValue().GetFields()
	if sv, ok := fields[name]; ok {
		return sv.GetIntegerValue() + int64(sv.GetDoubleValue())
	}
	for _, sv := range fields {
		if n := firstNamed(sv, name); n != 0 {
			return n
		}
	}
	for _, e := range v.GetArrayValue().GetValues() {
		if n := firstNamed(e, name); n != 0 {
			return n
		}
	}
	return 0
}

func integer(n int64) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: n}}
}

func str(s string) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: s}}
}
