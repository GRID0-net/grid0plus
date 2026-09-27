package toyohr

import (
	"context"
	"errors"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/proto/common"
	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// A player's cloud save is kept as one row of the Ugcstore document table,
// under the record's own resource name. That name lives outside
// "tenants/<t>/documents/", so Ugcstore's GetDocument and RunQuery never
// serve it, and the row's Fields column already is exactly what a save
// record's save_data is: a protobuf-encoded nn.npln.common.MapValue.

// saveLocks serializes the read-merge-write of one player's record. The
// console sends its writes concurrently (two identical Validate writes 150 ms
// apart in the 2026-09-24 loopback capture), and a lost update here is lost
// progression.
var saveLocks sync.Map // uid -> *sync.Mutex

func lockSave(uid string) func() {
	m, _ := saveLocks.LoadOrStore(uid, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Service) saveRecordName(uid string) string {
	return s.tenantPath() + "/saveRecords/" + uid
}

// loadSave returns the player's stored record, or (nil, nil) when there is
// none yet.
func (s *Service) loadSave(ctx context.Context, uid string) (*toyohrpb.SaveRecord, error) {
	d, err := s.store.GetUGCDocument(ctx, s.saveRecordName(uid))
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	data := &common.MapValue{}
	if err := proto.Unmarshal(d.Fields, data); err != nil {
		return nil, err
	}
	if data.Fields == nil {
		data.Fields = map[string]*common.Value{}
	}
	return &toyohrpb.SaveRecord{
		Name:       d.Name,
		SaveData:   data,
		CreateTime: timestamppb.New(d.CreateTime),
		UpdateTime: timestamppb.New(d.UpdateTime),
	}, nil
}

func (s *Service) putSave(ctx context.Context, uid string, rec *toyohrpb.SaveRecord) error {
	fields, err := proto.Marshal(rec.GetSaveData())
	if err != nil {
		return err
	}
	return s.store.PutUGCDocument(ctx, db.UGCDocument{
		Name:       rec.GetName(),
		Owner:      uid,
		Fields:     fields,
		CreateTime: rec.GetCreateTime().AsTime(),
		UpdateTime: rec.GetUpdateTime().AsTime(),
	})
}

// nextSaveVersion is the record's new update_time. The console presents the
// last one it was given as the next write's version token, so it must move
// strictly forward even when two writes land in the same instant -- and it is
// kept at microsecond precision because that is what the store round-trips.
func nextSaveVersion(now time.Time, prev *timestamppb.Timestamp) *timestamppb.Timestamp {
	t := now.UTC().Truncate(time.Microsecond)
	if prev != nil {
		if p := prev.AsTime(); !t.After(p) {
			t = p.Add(time.Microsecond)
		}
	}
	return timestamppb.New(t)
}

// mergeSaveDelta applies a WriteSaveRecord delta to stored save_data, using
// the four rules NextendoNetwork/splatoon-3 documents as reproduced against
// real Nintendo (replaying 24 captured deltas onto one served record yields
// the next served record exactly):
//
//   - a map merges into an existing map key by key, recursively;
//   - an explicit null deletes the key from its parent map;
//   - an empty array is stored as null;
//   - anything else replaces the stored value wholesale.
func mergeSaveDelta(dst, delta *common.MapValue) {
	if dst.Fields == nil {
		dst.Fields = map[string]*common.Value{}
	}
	for k, v := range delta.GetFields() {
		if _, isNull := v.GetValueType().(*common.Value_NullValue); isNull {
			delete(dst.Fields, k)
			continue
		}
		if dm := dst.Fields[k].GetMapValue(); dm != nil && v.GetMapValue() != nil {
			mergeSaveDelta(dm, v.GetMapValue())
			continue
		}
		dst.Fields[k] = storedSaveValue(v)
	}
}

func storedSaveValue(v *common.Value) *common.Value {
	switch x := v.GetValueType().(type) {
	case *common.Value_ArrayValue:
		if len(x.ArrayValue.GetValues()) == 0 {
			return &common.Value{ValueType: &common.Value_NullValue{}}
		}
		out := &common.ArrayValue{Values: make([]*common.Value, len(x.ArrayValue.GetValues()))}
		for i, e := range x.ArrayValue.GetValues() {
			out.Values[i] = storedSaveValue(e)
		}
		return &common.Value{ValueType: &common.Value_ArrayValue{ArrayValue: out}}
	case *common.Value_MapValue:
		out := &common.MapValue{Fields: make(map[string]*common.Value, len(x.MapValue.GetFields()))}
		for k, e := range x.MapValue.GetFields() {
			out.Fields[k] = storedSaveValue(e)
		}
		return &common.Value{ValueType: &common.Value_MapValue{MapValue: out}}
	}
	return proto.Clone(v).(*common.Value)
}
