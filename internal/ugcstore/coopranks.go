package ugcstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// The Salmon Run rank outside the game's own jobs: LAN jobs a mod accepted
// through the Discord bot, and the rotation reset. Both move the same
// CoopUserAttribute.total_grade_point online jobs move (ApplyCoopGrades), so
// a player who only ever plays over LAN opens the game online at the rank
// they earned there.

// evpBase is where Eggsecutive VP starts on the total coordinate
// (nextCoopGrade); its 0..999 points sit above it.
const evpBase = 800

// coopResetPoints is the points a title keeps at a new rotation: 40, or at
// Eggsecutive VP the stage badge's 200, 300 or 400 (Inkipedia, Salmon Run
// Next Wave, "Grizzco Rankings", since 6.0.0).
func coopResetPoints(title, stageBest int64) int64 {
	if title < 8 {
		return 40
	}
	switch {
	case stageBest >= 999:
		return 400
	case stageBest >= 600:
		return 300
	case stageBest >= 400:
		return 200
	}
	return 40
}

func coopAttributeName(tenant, uid string) string {
	return "tenants/" + tenant + documentsSegment + "services/GameRecord/users/" + uid + "/CoopUserAttribute/Data"
}

func coopBestName(root, uid string) string { return root + "grid0/coopBest/" + uid }

// raiseCoopBest is the write that records points as the shift's best
// Eggsecutive VP points, or nil when it is not a new best. The best is kept
// per shift rather than per stage: the stage of a shift is the schedule's to
// say (internal/toyohr), and ugcstore never reads the schedule.
func raiseCoopBest(ctx context.Context, store db.Store, root, uid, shift string, points int64, now time.Time) (*db.UGCDocumentCAS, error) {
	name := coopBestName(root, uid)
	d, err := store.GetUGCDocument(ctx, name)
	var previous *time.Time
	switch {
	case errors.Is(err, db.ErrNotFound):
		d = db.UGCDocument{Name: name, Owner: uid, CreateTime: now}
	case err != nil:
		return nil, err
	default:
		t := d.UpdateTime
		previous = &t
	}
	best, err := decodeFields(d.Fields)
	if err != nil {
		return nil, err
	}
	if v, ok := best.Fields[shift]; ok && v.GetIntegerValue() >= points {
		return nil, nil
	}
	best.Fields[shift] = integer(points)
	if d.Fields, err = encodeFields(best); err != nil {
		return nil, err
	}
	d.UpdateTime = now
	return &db.UGCDocumentCAS{Document: d, PreviousUpdateTime: previous}, nil
}

// CoopBests is every shift's best Eggsecutive VP points for uid, from GRID0+'s
// own record and from the game's point cards (best_grade_point), which hold
// the online shifts played before the record existed.
func CoopBests(ctx context.Context, store db.Store, tenant, uid string) (map[string]int64, error) {
	root := "tenants/" + tenant + documentsSegment
	out := map[string]int64{}
	raise := func(shift string, points int64) { out[shift] = max(out[shift], points) }
	cards, err := store.ListUGCDocuments(ctx, root+"services/GameRecord/users/"+uid+"/PointCardRegular/")
	if err != nil {
		return nil, err
	}
	for _, c := range cards {
		f, err := decodeFields(c.Fields)
		if err != nil {
			return nil, err
		}
		if best := f.Fields["best_grade_point"].GetIntegerValue(); best >= evpBase {
			raise(lastPathSegment(c.Name), min(best-evpBase, 999))
		}
	}
	d, err := store.GetUGCDocument(ctx, coopBestName(root, uid))
	if errors.Is(err, db.ErrNotFound) {
		return out, nil
	} else if err != nil {
		return nil, err
	}
	f, err := decodeFields(d.Fields)
	if err != nil {
		return nil, err
	}
	for shift, v := range f.Fields {
		raise(shift, v.GetIntegerValue())
	}
	return out, nil
}

func lastPathSegment(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[i+1:]
		}
	}
	return name
}

// CoopGradePoint is uid's total grade coordinate and the shift it belongs
// to, 0 and "" for a player who never played.
func CoopGradePoint(ctx context.Context, store db.Store, tenant, uid string) (int64, string, error) {
	d, err := store.GetUGCDocument(ctx, coopAttributeName(tenant, uid))
	if errors.Is(err, db.ErrNotFound) {
		return 0, "", nil
	} else if err != nil {
		return 0, "", err
	}
	f, err := decodeFields(d.Fields)
	if err != nil {
		return 0, "", err
	}
	return f.Fields["total_grade_point"].GetIntegerValue(), f.Fields["shift_id"].GetStringValue(), nil
}

// RollCoopRotation moves uid into shift: if the stored shift is a different
// one, the title is kept and its points reset (coopResetPoints, with
// stageBest the player's best Eggsecutive VP points on the new shift's
// stage). The game sends InitializeAttributes coop_shift several times for
// one shift, so the same shift twice must change nothing. A player with no
// stored shift has had nothing to reset.
//
// TODO(capture-needed): that Nintendo's server applies the reset when the
// game sends coop_shift. The console never sends a grade (grid0's logs,
// 2026-09-20..10-17: coop_shift only, never coop_grade), so the server must
// apply it somewhere, and coop_shift is the only rotation signal it gets. A
// capture of a console's first job in a new rotation against Nintendo would
// settle it; change RollCoopRotation.
func RollCoopRotation(ctx context.Context, store db.Store, tenant, uid, shift string, stageBest int64, now time.Time) (bool, error) {
	name := coopAttributeName(tenant, uid)
	for attempt := 0; attempt < 4; attempt++ {
		d, err := store.GetUGCDocument(ctx, name)
		var previous *time.Time
		switch {
		case errors.Is(err, db.ErrNotFound):
			d = db.UGCDocument{Name: name, Owner: uid, CreateTime: now}
		case err != nil:
			return false, err
		default:
			t := d.UpdateTime
			previous = &t
		}
		attrs, err := decodeFields(d.Fields)
		if err != nil {
			return false, err
		}
		stored := attrs.Fields["shift_id"].GetStringValue()
		if stored == shift {
			return false, nil
		}
		reset := false
		if stored != "" {
			before := attrs.Fields["total_grade_point"].GetIntegerValue()
			if before < 0 || before > 1799 {
				return false, fmt.Errorf("invalid stored Coop grade coordinate")
			}
			title := min(before/100, 8)
			after := title*100 + coopResetPoints(title, stageBest)
			attrs.Fields["total_grade_point"] = integer(after)
			attrs.Fields["grade"] = integer(title)
			reset = after != before
		}
		attrs.Fields["shift_id"] = str(shift)
		if d.Fields, err = encodeFields(attrs); err != nil {
			return false, err
		}
		d.UpdateTime = now
		err = store.CompareAndPutUGCDocuments(ctx, []db.UGCDocumentCAS{{Document: d, PreviousUpdateTime: previous}})
		if errors.Is(err, db.ErrConflict) {
			continue
		}
		return reset, err
	}
	return false, db.ErrConflict
}

// CreditLANCoopGrade applies one accepted LAN job to uid's rank, once per
// job id however many times it is called. The Salmometer does not move.
func CreditLANCoopGrade(ctx context.Context, store db.Store, tenant, job, uid string, delta int64, now time.Time) (before, after int64, err error) {
	grades, err := creditCoopGrade(ctx, store, tenant, "lan-"+job, uid, delta, nil, now)
	if err != nil {
		return 0, 0, err
	}
	return grades.Fields["grade_point_before"].GetIntegerValue(), grades.Fields["grade_point_after"].GetIntegerValue(), nil
}
