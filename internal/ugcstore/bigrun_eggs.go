package ugcstore

import (
	"context"
	"errors"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
)

// A special Big Run (BigRunParams.IsSpecial) shows a community golden-egg goal:
// the game compares a world total with 700,000,000, hard-coded in its main, and
// pays a bonus once it is reached. It reads the total with
// GlobalCounterService/GetSeason (four calls on entering the hall) and never adds
// to it itself, so the server keeps it: every Big Run job adds the golden eggs its
// team delivered, once per job, and the total starts again from zero at the next
// Big Run.
//
// UNCONFIRMED: the counter and season names GetSeason asks for, and whether
// Nintendo's total also counts non-special Big Runs. One running total is kept
// for the latest Big Run whatever name is asked for. Change: AddBigRunEggs and
// BigRunEggs.
const bigRunEggsDoc = "grid0/globalCounter/bigRunEggs"

// AddBigRunEggs adds one finished Big Run job's delivered golden eggs to the
// total, once per job however many consoles report it.
func AddBigRunEggs(ctx context.Context, store db.Store, tenant, battleID, shiftID string, eggs int64, now time.Time) error {
	if battleID == "" || shiftID == "" {
		return nil
	}
	root := "tenants/" + tenant + documentsSegment
	marker := root + "grid0/globalCounter/jobs/" + battleID
	if _, err := store.GetUGCDocument(ctx, marker); err == nil {
		return nil
	} else if !errors.Is(err, db.ErrNotFound) {
		return err
	}
	f, err := loadCard(ctx, store, root+bigRunEggsDoc)
	if err != nil {
		return err
	}
	if f["shift_id"].GetStringValue() != shiftID {
		f = map[string]*commonpb.Value{}
	}
	f["shift_id"] = str(shiftID)
	f["count"] = integer(f["count"].GetIntegerValue() + eggs)
	f["jobs"] = integer(f["jobs"].GetIntegerValue() + 1)
	if err := saveCard(ctx, store, root+bigRunEggsDoc, "", f, now); err != nil {
		return err
	}
	return store.PutUGCDocument(ctx, db.UGCDocument{Name: marker, Fields: []byte{}, CreateTime: now, UpdateTime: now})
}

// BigRunEggs returns the running total and the number of jobs behind it.
func BigRunEggs(ctx context.Context, store db.Store, tenant string) (count, jobs int64, err error) {
	f, err := loadCard(ctx, store, "tenants/"+tenant+documentsSegment+bigRunEggsDoc)
	if err != nil {
		return 0, 0, err
	}
	return f["count"].GetIntegerValue(), f["jobs"].GetIntegerValue(), nil
}
