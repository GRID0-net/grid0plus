package rendezvous

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

// Records is the state a title keeps between sessions, JSON-encoded into
// db.NEXRecord. One kind per thing kept ("unique_id", "fest_vote", ...).
type Records struct {
	store db.Store
	title string
	now   func() time.Time
}

// Records returns the server's record store.
func (s *Server) Records() *Records { return &Records{store: s.store, title: s.title.Name, now: s.now} }

// ErrNoRecord is a record that does not exist.
var ErrNoRecord = db.ErrNotFound

func recordCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// Put stores v under (kind, key), owned by owner ("" for none).
func (r *Records) Put(kind, key, owner string, v any) error {
	if r.store == nil {
		return errors.New("rendezvous: no store configured")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := recordCtx()
	defer cancel()
	return r.store.PutNEXRecord(ctx, db.NEXRecord{Title: r.title, Kind: kind, Key: key, Owner: owner,
		Value: b, UpdatedAt: r.now().UTC()})
}

// Get loads (kind, key) into v. ErrNoRecord if there is none.
func (r *Records) Get(kind, key string, v any) (db.NEXRecord, error) {
	if r.store == nil {
		return db.NEXRecord{}, ErrNoRecord
	}
	ctx, cancel := recordCtx()
	defer cancel()
	rec, err := r.store.GetNEXRecord(ctx, r.title, kind, key)
	if err != nil {
		return rec, err
	}
	return rec, json.Unmarshal(rec.Value, v)
}

// Delete removes (kind, key). A record that is not there is not an error.
func (r *Records) Delete(kind, key string) error {
	if r.store == nil {
		return nil
	}
	ctx, cancel := recordCtx()
	defer cancel()
	if err := r.store.DeleteNEXRecord(ctx, r.title, kind, key); err != nil && !errors.Is(err, db.ErrNotFound) {
		return err
	}
	return nil
}

// List returns a kind's records (optionally only owner's), most recently
// updated first, each decoded as a T. A record that does not decode is
// skipped rather than failing the list: one bad row must not hide the rest.
func List[T any](r *Records, kind, owner string) ([]T, error) {
	if r.store == nil {
		return nil, nil
	}
	ctx, cancel := recordCtx()
	defer cancel()
	recs, err := r.store.ListNEXRecords(ctx, db.NEXRecordQuery{Title: r.title, Kind: kind, Owner: owner})
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(recs))
	for _, rec := range recs {
		var v T
		if json.Unmarshal(rec.Value, &v) == nil {
			out = append(out, v)
		}
	}
	return out, nil
}

// ListRaw is List without the decoding, for a caller that needs each
// record's owner or key as well as its value.
func (r *Records) ListRaw(kind string) ([]db.NEXRecord, error) {
	if r.store == nil {
		return nil, nil
	}
	ctx, cancel := recordCtx()
	defer cancel()
	return r.store.ListNEXRecords(ctx, db.NEXRecordQuery{Title: r.title, Kind: kind})
}
