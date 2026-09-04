// Package gamesyncstore is the document store behind NPLN's Gamesync
// service (nn.npln.gamesync.v1.Gamesync) -- a Firestore-shaped key/value
// store the console reads and writes lobby and match state through.
//
// It works directly in terms of the generated proto types
// (proto/gamesync/v1.Document, proto/common.Value) rather than a private
// representation, for the same reason internal/toyohr passes proto messages
// around rather than inventing Go structs to convert to and from: there is no
// second shape to keep in sync, and the wire format the console reads is
// exactly what this package stores.
//
// Two things are deliberately simplified rather than guessed at, because
// nothing about Gamesync's *semantics* -- as opposed to its wire shapes,
// which the generated proto pins down exactly -- is documented anywhere this
// project could find:
//
//   - Transactions are atomic (every write in a commit applies, or none do)
//     but not isolated: a precondition is checked against whatever is in the
//     store at commit time, not a snapshot taken at BeginTransaction. Two
//     transactions open at once can each see the other's writes. A handful of
//     consoles sharing one lobby's documents is not the concurrency load
//     snapshot isolation exists for, and building it without a capture to
//     check the retry/conflict behaviour against would be guessing at
//     Nintendo internals to solve a problem this deployment does not have.
//   - FieldTransform's LoadGlobalValue/StoreGlobalValue/ClearGlobalValue have
//     no Firestore equivalent and no public documentation. They are accepted
//     and answered as no-ops (a null result) rather than rejected, so a
//     WriteDocuments call that includes one does not fail outright over a
//     transform nothing here understands. See TODO(capture-needed) on
//     transformGlobalValue.
//
// DefermentOperation is accepted at the RPC layer (internal/gamesync) and
// discarded, for the same reason: no source found documents what a
// "deferment" is for, and inventing a plausible Nintendo internal is exactly
// what CLAUDE.md's sixth ground rule forbids.
package gamesyncstore

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	gstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	v1 "github.com/n-popescu/switchnet/proto/gamesync/v1"
)

// ChangeType describes what happened to a document a Watch subscriber cares
// about.
type ChangeType int

const (
	Created ChangeType = iota
	Updated
	Deleted
)

// Change is one document mutation, delivered to every active Subscribe
// channel.
type Change struct {
	Name     string
	Type     ChangeType
	Document *v1.Document // nil when Type is Deleted
}

// changeBuffer is how many undelivered Changes a slow subscriber tolerates
// before new ones are dropped for it. A dropped change is not silently wrong
// -- internal/gamesync's KeepUserSession sends a fresh Document as EXIST when
// a target is (re-)registered, so a client that fell behind still converges
// the next time it lists a target, the same way a Firestore client resyncs
// after a RESET.
const changeBuffer = 64

// Store is a tenant's whole document tree. Safe for concurrent use.
type Store struct {
	mu   sync.RWMutex
	docs map[string]*v1.Document

	subMu sync.Mutex
	subs  map[chan Change]struct{}

	txMu sync.Mutex
	open map[string]struct{}

	now func() time.Time
}

// New returns an empty Store. now is overridable in tests.
func New(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{
		docs: map[string]*v1.Document{},
		subs: map[chan Change]struct{}{},
		open: map[string]struct{}{},
		now:  now,
	}
}

// Get returns the document at name, or nil if it does not exist.
func (s *Store) Get(name string) *v1.Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneDocument(s.docs[name])
}

// Read returns one result per requested name, in order, distinguishing a
// found document from a missing one the way ReadDocumentsResponse.Results
// does.
func (s *Store) Read(names []string) []*v1.Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*v1.Document, len(names))
	for i, n := range names {
		out[i] = cloneDocument(s.docs[n])
	}
	return out
}

// List returns the documents directly under parent (one path segment below
// it), sorted by name for a stable page order, starting after pageToken (the
// name of the last document a previous call returned; empty means start from
// the beginning). It returns at most pageSize documents (0 or negative means
// unlimited) and the token to resume from, empty when the list is exhausted.
func (s *Store) List(parent string, pageSize int32, pageToken string) (docs []*v1.Document, nextToken string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var names []string
	for name := range s.docs {
		if parentOf(name) == parent {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	start := 0
	if pageToken != "" {
		start = sort.SearchStrings(names, pageToken)
		if start < len(names) && names[start] == pageToken {
			start++
		}
	}
	names = names[start:]

	if pageSize > 0 && int(pageSize) < len(names) {
		nextToken = names[pageSize-1]
		names = names[:pageSize]
	}

	docs = make([]*v1.Document, len(names))
	for i, n := range names {
		docs[i] = cloneDocument(s.docs[n])
	}
	return docs, nextToken
}

// CollectionIDs returns the distinct collection IDs directly under document
// (the empty string for the root). Order is not significant; pagination in
// the RPC layer is done over this slice.
func (s *Store) CollectionIDs(document string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := document
	if prefix != "" {
		prefix += "/"
	}
	seen := map[string]bool{}
	var ids []string
	for name := range s.docs {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		id := rest[:strings.IndexByte(rest+"/", '/')]
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Update replaces document (or the fields update_mask names, if it is
// non-empty) at doc.Name, subject to precondition. A document that does not
// exist yet is created.
func (s *Store) Update(doc *v1.Document, mask *fieldmaskpb.FieldMask, precondition *v1.Precondition) (*v1.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyUpdate(doc, mask, precondition)
}

func (s *Store) applyUpdate(doc *v1.Document, mask *fieldmaskpb.FieldMask, precondition *v1.Precondition) (*v1.Document, error) {
	existing := s.docs[doc.GetName()]
	if err := checkPrecondition(existing, precondition); err != nil {
		return nil, err
	}

	now := timestamppb.New(s.now())
	out := &v1.Document{Name: doc.GetName(), UpdateTime: now}
	if existing != nil {
		out.CreateTime = existing.CreateTime
	} else {
		out.CreateTime = now
	}

	if mask == nil || len(mask.GetPaths()) == 0 {
		out.Fields = cloneMapValue(doc.GetFields())
	} else {
		fields := map[string]*commonpb.Value{}
		if existing != nil {
			fields = cloneFields(existing.GetFields().GetFields())
		}
		for _, path := range mask.GetPaths() {
			segments := strings.Split(path, ".")
			if v := getPath(doc.GetFields(), segments); v != nil {
				setPath(fields, segments, v)
			} else {
				deletePath(fields, segments)
			}
		}
		out.Fields = &commonpb.MapValue{Fields: fields}
	}

	s.set(out, existing == nil)
	return cloneDocument(out), nil
}

// Merge deep-merges doc's fields into whatever is stored at doc.Name (or
// creates it, if nothing is). A field the incoming document does not mention
// is left untouched; a nested map is merged recursively rather than replaced
// wholesale, matching Firestore's own set-with-merge semantics.
func (s *Store) Merge(doc *v1.Document) (*v1.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyMerge(doc), nil
}

func (s *Store) applyMerge(doc *v1.Document) *v1.Document {
	existing := s.docs[doc.GetName()]
	now := timestamppb.New(s.now())
	out := &v1.Document{Name: doc.GetName(), UpdateTime: now}
	if existing != nil {
		out.CreateTime = existing.CreateTime
		out.Fields = &commonpb.MapValue{Fields: mergeFields(existing.GetFields().GetFields(), doc.GetFields().GetFields())}
	} else {
		out.CreateTime = now
		out.Fields = cloneMapValue(doc.GetFields())
	}

	s.set(out, existing == nil)
	return cloneDocument(out)
}

// Transform applies field transforms to the document at name, in request
// order, and returns the resulting document along with the value each
// transform actually wrote -- WriteResult.TransformResult.Results is exactly
// this slice. A transform against a document that does not yet exist creates
// it, the same as Firestore's transform-implies-set behaviour.
func (s *Store) Transform(name string, transforms []*v1.FieldTransform) (*v1.Document, []*commonpb.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyTransformDoc(name, transforms)
}

func (s *Store) applyTransformDoc(name string, transforms []*v1.FieldTransform) (*v1.Document, []*commonpb.Value, error) {
	existing := s.docs[name]
	fields := map[string]*commonpb.Value{}
	var createTime *timestamppb.Timestamp
	if existing != nil {
		fields = cloneFields(existing.GetFields().GetFields())
		createTime = existing.CreateTime
	}

	now := s.now()
	if createTime == nil {
		createTime = timestamppb.New(now)
	}

	results := make([]*commonpb.Value, len(transforms))
	for i, t := range transforms {
		segments := strings.Split(t.GetFieldPath(), ".")
		results[i] = applyTransform(fields, segments, t, now)
	}

	out := &v1.Document{Name: name, Fields: &commonpb.MapValue{Fields: fields}, CreateTime: createTime, UpdateTime: timestamppb.New(now)}
	s.set(out, existing == nil)
	return cloneDocument(out), results, nil
}

// Delete removes the document at name, subject to precondition. Deleting a
// document that does not exist, with no precondition, is not an error --
// Firestore's own DeleteDocument is idempotent the same way.
func (s *Store) Delete(name string, precondition *v1.Precondition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyDelete(name, precondition)
}

func (s *Store) applyDelete(name string, precondition *v1.Precondition) error {
	existing := s.docs[name]
	if err := checkPrecondition(existing, precondition); err != nil {
		return err
	}
	if existing == nil {
		return nil
	}
	delete(s.docs, name)
	s.publish(Change{Name: name, Type: Deleted})
	return nil
}

// set stores doc and publishes the change. Callers hold s.mu.
func (s *Store) set(doc *v1.Document, created bool) {
	s.docs[doc.GetName()] = doc
	typ := Updated
	if created {
		typ = Created
	}
	s.publish(Change{Name: doc.GetName(), Type: typ, Document: cloneDocument(doc)})
}

// Subscribe returns a channel of every Change from this point on, and a
// cancel function that must be called when the subscriber is done.
func (s *Store) Subscribe() (<-chan Change, func()) {
	ch := make(chan Change, changeBuffer)
	s.subMu.Lock()
	s.subs[ch] = struct{}{}
	s.subMu.Unlock()

	cancel := func() {
		s.subMu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.subMu.Unlock()
	}
	return ch, cancel
}

func (s *Store) publish(c Change) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- c:
		default:
			// Slow subscriber: drop rather than block every writer on one
			// stalled KeepUserSession stream. See changeBuffer's comment.
		}
	}
}

// BeginTransaction opens a transaction and returns its opaque token.
func (s *Store) BeginTransaction() []byte {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	token := hex.EncodeToString(raw[:])

	s.txMu.Lock()
	s.open[token] = struct{}{}
	s.txMu.Unlock()
	return []byte(token)
}

// RollbackTransaction discards a transaction. Since no write is applied until
// Commit, this only forgets the token -- there is nothing to undo.
func (s *Store) RollbackTransaction(token []byte) {
	s.txMu.Lock()
	delete(s.open, string(token))
	s.txMu.Unlock()
}

// CommitTransaction validates that token is still open, then applies ops
// atomically: every operation's precondition is checked against the current
// store before any of them writes, so the whole batch either lands together
// or fails together with nothing changed. See the package doc for what this
// does and does not guarantee relative to real Firestore transactions.
func (s *Store) CommitTransaction(token []byte, ops []*v1.WriteOperation) ([]*v1.WriteResult, error) {
	s.txMu.Lock()
	_, ok := s.open[string(token)]
	if ok {
		delete(s.open, string(token))
	}
	s.txMu.Unlock()
	if !ok {
		return nil, gstatus.Error(codes.FailedPrecondition, "transaction is not open")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Phase 1: every precondition must hold against the current store.
	for _, op := range ops {
		var name string
		var cond *v1.Precondition
		switch t := op.GetOperationType().(type) {
		case *v1.WriteOperation_UpdateDocument:
			name, cond = t.UpdateDocument.GetDocument().GetName(), t.UpdateDocument.GetCurrentDocument()
		case *v1.WriteOperation_DeleteDocument:
			name, cond = t.DeleteDocument.GetName(), t.DeleteDocument.GetCurrentDocument()
		default:
			continue
		}
		if err := checkPrecondition(s.docs[name], cond); err != nil {
			return nil, err
		}
	}

	// Phase 2: apply. Preconditions already passed, so none of these can fail.
	results := make([]*v1.WriteResult, len(ops))
	for i, op := range ops {
		results[i] = s.applyWriteLocked(op)
	}
	return results, nil
}

// applyWriteLocked applies one WriteOperation. Callers hold s.mu.
//
// Preconditions are not rechecked here: CommitTransaction already validated
// every operation's precondition against the current store in its first
// pass, under the same continuously-held lock, so nothing has changed by the
// time this runs. WriteDocuments (internal/gamesync) applies each operation
// through its own precondition-checking method instead of this one, so a
// non-transactional write still enforces its precondition.
func (s *Store) applyWriteLocked(op *v1.WriteOperation) *v1.WriteResult {
	switch t := op.GetOperationType().(type) {
	case *v1.WriteOperation_UpdateDocument:
		_, _ = s.applyUpdate(t.UpdateDocument.GetDocument(), t.UpdateDocument.GetUpdateMask(), nil)
		return &v1.WriteResult{ResultType: &v1.WriteResult_UpdateResult{UpdateResult: &v1.UpdateResult{}}}
	case *v1.WriteOperation_MergeDocument:
		s.applyMerge(t.MergeDocument.GetDocument())
		return &v1.WriteResult{ResultType: &v1.WriteResult_UpdateResult{UpdateResult: &v1.UpdateResult{}}}
	case *v1.WriteOperation_TransformDocument:
		_, results, _ := s.applyTransformDoc(t.TransformDocument.GetName(), t.TransformDocument.GetFieldTransforms())
		return &v1.WriteResult{ResultType: &v1.WriteResult_TransformResult{TransformResult: &v1.TransformResult{Results: results}}}
	case *v1.WriteOperation_DeleteDocument:
		_ = s.applyDelete(t.DeleteDocument.GetName(), nil)
		return &v1.WriteResult{ResultType: &v1.WriteResult_DeleteResult{DeleteResult: &v1.DeleteResult{}}}
	}
	return &v1.WriteResult{}
}

func checkPrecondition(existing *v1.Document, cond *v1.Precondition) error {
	if cond == nil {
		return nil
	}
	switch t := cond.GetConditionType().(type) {
	case *v1.Precondition_Exists:
		if t.Exists != (existing != nil) {
			if t.Exists {
				return gstatus.Error(codes.NotFound, "document does not exist")
			}
			return gstatus.Error(codes.AlreadyExists, "document already exists")
		}
	case *v1.Precondition_UpdateTime:
		if existing == nil {
			return gstatus.Error(codes.NotFound, "document does not exist")
		}
		if !existing.GetUpdateTime().AsTime().Equal(t.UpdateTime.AsTime()) {
			return gstatus.Error(codes.FailedPrecondition, "document has been modified")
		}
	}
	return nil
}

// FilterFields returns a MapValue containing only the fields named in paths
// (dot-separated for a nested field), reusing the exact same path resolution
// Update's own field mask uses. An empty paths returns a full copy of
// fields -- the same "no mask means everything" convention.
func FilterFields(fields *commonpb.MapValue, paths []string) *commonpb.MapValue {
	if len(paths) == 0 {
		return cloneMapValue(fields)
	}
	out := map[string]*commonpb.Value{}
	for _, p := range paths {
		segments := strings.Split(p, ".")
		if v := getPath(fields, segments); v != nil {
			setPath(out, segments, v)
		}
	}
	return &commonpb.MapValue{Fields: out}
}

func parentOf(name string) string {
	i := strings.LastIndex(name, "/")
	if i < 0 {
		return ""
	}
	return name[:i]
}

func getPath(v *commonpb.MapValue, segments []string) *commonpb.Value {
	if v == nil {
		return nil
	}
	cur, ok := v.GetFields()[segments[0]]
	if !ok {
		return nil
	}
	if len(segments) == 1 {
		return cur
	}
	return getPath(cur.GetMapValue(), segments[1:])
}

// setPath and deletePath always build a fresh map at every level they
// descend into, rather than mutating whatever nested MapValue is already
// there -- the same "never mutate, always produce a new map" rule
// mergeFields below already follows. Callers such as applyUpdate only
// shallow-clone the top level of an existing document (cloneFields), so a
// nested MapValue reached from here can be the exact same Go map object
// already handed out to a caller by an earlier Get()/Read() (also only
// shallow-cloned, by cloneDocument). Mutating that map in place -- as an
// earlier version of both functions did -- raced a concurrent reader of
// that same shared map (a stream proto-marshaling a document it fetched
// earlier) against this write, under nothing but Store's own mutex, which
// protects the top-level docs map, not structure that has already escaped
// through a returned *v1.Document.
func setPath(fields map[string]*commonpb.Value, segments []string, val *commonpb.Value) {
	if len(segments) == 1 {
		fields[segments[0]] = val
		return
	}
	childFields := map[string]*commonpb.Value{}
	if child, ok := fields[segments[0]]; ok && child.GetMapValue() != nil {
		childFields = cloneFields(child.GetMapValue().Fields)
	}
	childMap := &commonpb.MapValue{Fields: childFields}
	fields[segments[0]] = &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: childMap}}
	setPath(childMap.Fields, segments[1:], val)
}

func deletePath(fields map[string]*commonpb.Value, segments []string) {
	if len(segments) == 1 {
		delete(fields, segments[0])
		return
	}
	child, ok := fields[segments[0]]
	if !ok {
		return
	}
	cm := child.GetMapValue()
	if cm == nil {
		return
	}
	childMap := &commonpb.MapValue{Fields: cloneFields(cm.Fields)}
	fields[segments[0]] = &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: childMap}}
	deletePath(childMap.Fields, segments[1:])
}

func mergeFields(dst, src map[string]*commonpb.Value) map[string]*commonpb.Value {
	out := make(map[string]*commonpb.Value, len(dst)+len(src))
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		if existing, ok := out[k]; ok {
			if em, sm := existing.GetMapValue(), v.GetMapValue(); em != nil && sm != nil {
				out[k] = &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{
					Fields: mergeFields(em.Fields, sm.Fields),
				}}}
				continue
			}
		}
		out[k] = v
	}
	return out
}

func cloneDocument(d *v1.Document) *v1.Document {
	if d == nil {
		return nil
	}
	return &v1.Document{
		Name:       d.Name,
		Fields:     cloneMapValue(d.Fields),
		CreateTime: d.CreateTime,
		UpdateTime: d.UpdateTime,
	}
}

func cloneMapValue(m *commonpb.MapValue) *commonpb.MapValue {
	if m == nil {
		return nil
	}
	return &commonpb.MapValue{Fields: cloneFields(m.Fields)}
}

func cloneFields(fields map[string]*commonpb.Value) map[string]*commonpb.Value {
	out := make(map[string]*commonpb.Value, len(fields))
	for k, v := range fields {
		out[k] = v
	}
	return out
}

// applyTransform applies one FieldTransform to fields in place and returns
// the value it wrote -- the value WriteResult.TransformResult.Results
// reports for this transform.
func applyTransform(fields map[string]*commonpb.Value, segments []string, t *v1.FieldTransform, now time.Time) *commonpb.Value {
	switch tt := t.GetTransformType().(type) {
	case *v1.FieldTransform_SetServerValue:
		if tt.SetServerValue != v1.FieldTransform_REQUEST_TIME {
			return nullValue()
		}
		val := &commonpb.Value{ValueType: &commonpb.Value_TimestampValue{TimestampValue: timestamppb.New(now)}}
		setPath(fields, segments, val)
		return val
	case *v1.FieldTransform_Increment:
		return applyNumericTransform(fields, segments, tt.Increment, func(existing, operand float64) float64 { return existing + operand })
	case *v1.FieldTransform_Maximum:
		return applyNumericTransform(fields, segments, tt.Maximum, func(existing, operand float64) float64 {
			if existing > operand {
				return existing
			}
			return operand
		})
	case *v1.FieldTransform_Minimum:
		return applyNumericTransform(fields, segments, tt.Minimum, func(existing, operand float64) float64 {
			if existing < operand {
				return existing
			}
			return operand
		})
	default:
		// LoadGlobalValue / StoreGlobalValue / ClearGlobalValue.
		// TODO(capture-needed): "global value" transforms have no Firestore
		// equivalent and no public documentation of what a gamesync "global
		// value" is scoped to, or what loading one that was never stored
		// should return. Answered as a no-op null result rather than guessed
		// at -- see the package doc.
		return nullValue()
	}
}

// applyNumericTransform reads the numeric value currently at segments
// (treating a missing or non-numeric field as 0, the same as Firestore's own
// Increment), combines it with operand via combine, writes the result back,
// and returns it. The result is a double if either input was, otherwise an
// integer -- again matching Firestore's promotion rule.
func applyNumericTransform(
	fields map[string]*commonpb.Value, segments []string, operand *commonpb.Value,
	combine func(existing, operand float64) float64,
) *commonpb.Value {
	opF, opFloat, ok := numericOf(operand)
	if !ok {
		return nullValue()
	}

	existF, existFloat := 0.0, false
	if existing := getPath(&commonpb.MapValue{Fields: fields}, segments); existing != nil {
		if f, isFloat, ok := numericOf(existing); ok {
			existF, existFloat = f, isFloat
		}
	}

	val := numericValue(combine(existF, opF), existFloat || opFloat)
	setPath(fields, segments, val)
	return val
}

func numericOf(v *commonpb.Value) (value float64, isFloat, ok bool) {
	switch vt := v.GetValueType().(type) {
	case *commonpb.Value_IntegerValue:
		return float64(vt.IntegerValue), false, true
	case *commonpb.Value_DoubleValue:
		return vt.DoubleValue, true, true
	case *commonpb.Value_FloatValue:
		return float64(vt.FloatValue), true, true
	default:
		return 0, false, false
	}
}

func numericValue(f float64, isFloat bool) *commonpb.Value {
	if isFloat {
		return &commonpb.Value{ValueType: &commonpb.Value_DoubleValue{DoubleValue: f}}
	}
	return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: int64(f)}}
}

func nullValue() *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_NullValue{NullValue: structpb.NullValue_NULL_VALUE}}
}
