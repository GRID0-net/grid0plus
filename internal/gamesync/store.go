package gamesync

import (
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	syncpb "github.com/n-popescu/switchnet/proto/gamesync/v1"
)

// store is the document tree.
//
// One lock over the whole tree, held across a whole batch. Finer locking would
// be faster and wrong: WriteDocuments is atomic, and a per-document lock means
// a reader can observe half a batch — which is exactly the state a client has no
// way to interpret.
type store struct {
	now func() time.Time

	mu   sync.RWMutex
	docs map[string]*syncpb.Document
}

func newStore(now func() time.Time) *store {
	return &store{now: now, docs: map[string]*syncpb.Document{}}
}

func (s *store) get(name string) (*syncpb.Document, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	doc, ok := s.docs[name]
	if !ok {
		return nil, false
	}
	return doc, true
}

// list returns the documents directly under a collection path.
//
// Directly: "a/b/c" is under "a/b" and "a/b/c/d/e" is not. A recursive list
// would return a lobby's whole subtree to a client that asked for one level,
// and the client cannot tell the difference from the response.
func (s *store) list(parent string) []*syncpb.Document {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := strings.TrimSuffix(parent, "/") + "/"
	var out []*syncpb.Document
	for _, name := range sortedNames(s.docs) {
		if parent != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		if strings.Contains(rest, "/") {
			continue
		}
		out = append(out, s.docs[name])
	}
	return out
}

// collections returns the collection names directly under a document.
func (s *store) collections(document string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := strings.TrimSuffix(document, "/") + "/"
	seen := map[string]struct{}{}
	for name := range s.docs {
		if document != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		// A collection is the segment between this document and the next one,
		// so a child document "a/b/coll/doc" contributes "coll".
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) == 2 && parts[0] != "" {
			seen[parts[0]] = struct{}{}
		}
	}
	return sortedNames(seen)
}

// commit is what one applied batch produced.
type commit struct {
	at      time.Time
	changes []*syncpb.DocumentChange
}

// apply runs a batch of write operations atomically.
//
// Two passes. The first checks every precondition against the current tree; the
// second mutates. A batch that fails changes nothing, because a half-applied
// batch leaves the client's next read showing a state neither side intended and
// no way to tell which half landed.
func (s *store) apply(ops []*syncpb.WriteOperation) ([]*syncpb.WriteResult, commit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	for _, op := range ops {
		if err := s.checkPrecondition(op); err != nil {
			return nil, commit{}, err
		}
	}

	out := commit{at: now}
	results := make([]*syncpb.WriteResult, 0, len(ops))

	for _, op := range ops {
		switch o := op.GetOperationType().(type) {
		case *syncpb.WriteOperation_UpdateDocument:
			doc := s.writeDocument(o.UpdateDocument.GetDocument(),
				o.UpdateDocument.GetUpdateMask().GetPaths(), now)
			results = append(results, updateResult())
			out.changes = append(out.changes, changeFor(doc, syncpb.DocumentChange_UPDATED))

		case *syncpb.WriteOperation_MergeDocument:
			// A merge with no mask is a mask of every field the client sent.
			doc := s.writeDocument(o.MergeDocument.GetDocument(),
				fieldNames(o.MergeDocument.GetDocument()), now)
			results = append(results, updateResult())
			out.changes = append(out.changes, changeFor(doc, syncpb.DocumentChange_UPDATED))

		case *syncpb.WriteOperation_TransformDocument:
			doc, values, err := s.transform(o.TransformDocument, now)
			if err != nil {
				// Reached only for a transform this code does not implement,
				// which the precondition pass cannot detect. The batch has
				// already partly applied at this point, so the check that
				// matters is in checkPrecondition -- see transformSupported.
				return nil, commit{}, err
			}
			results = append(results, &syncpb.WriteResult{
				ResultType: &syncpb.WriteResult_TransformResult{
					TransformResult: &syncpb.TransformResult{Results: values},
				},
			})
			out.changes = append(out.changes, changeFor(doc, syncpb.DocumentChange_UPDATED))

		case *syncpb.WriteOperation_DeleteDocument:
			name := o.DeleteDocument.GetName()
			doc, existed := s.docs[name]
			delete(s.docs, name)
			results = append(results, &syncpb.WriteResult{
				ResultType: &syncpb.WriteResult_DeleteResult{DeleteResult: &syncpb.DeleteResult{}},
			})
			if existed {
				out.changes = append(out.changes, changeFor(doc, syncpb.DocumentChange_DELETED))
			}

		default:
			return nil, commit{}, status.Error(codes.InvalidArgument, "unsupported write operation")
		}
	}

	return results, out, nil
}

// checkPrecondition validates one operation against the current tree.
func (s *store) checkPrecondition(op *syncpb.WriteOperation) error {
	var name string
	var pre *syncpb.Precondition

	switch o := op.GetOperationType().(type) {
	case *syncpb.WriteOperation_UpdateDocument:
		name = o.UpdateDocument.GetDocument().GetName()
		pre = o.UpdateDocument.GetCurrentDocument()
	case *syncpb.WriteOperation_DeleteDocument:
		name = o.DeleteDocument.GetName()
		pre = o.DeleteDocument.GetCurrentDocument()
	case *syncpb.WriteOperation_MergeDocument:
		if o.MergeDocument.GetDocument().GetName() == "" {
			return status.Error(codes.InvalidArgument, "merge with no document name")
		}
		return nil
	case *syncpb.WriteOperation_TransformDocument:
		// Checked here rather than during the mutation pass: a transform this
		// code does not implement would otherwise fail halfway through a batch
		// that the atomicity guarantee says cannot half-apply.
		for _, ft := range o.TransformDocument.GetFieldTransforms() {
			if !transformSupported(ft) {
				return status.Errorf(codes.Unimplemented,
					"field transform %T is not implemented", ft.GetTransformType())
			}
		}
		if o.TransformDocument.GetName() == "" {
			return status.Error(codes.InvalidArgument, "transform with no document name")
		}
		return nil
	default:
		return status.Error(codes.InvalidArgument, "unsupported write operation")
	}

	if name == "" {
		return status.Error(codes.InvalidArgument, "write with no document name")
	}
	if pre == nil {
		return nil
	}

	doc, exists := s.docs[name]
	switch c := pre.GetConditionType().(type) {
	case *syncpb.Precondition_Exists:
		if c.Exists != exists {
			// FailedPrecondition, not AlreadyExists or NotFound: the client set
			// a condition and it did not hold, which is one situation however
			// it failed.
			return status.Error(codes.FailedPrecondition, "document precondition failed")
		}
	case *syncpb.Precondition_UpdateTime:
		if !exists || !doc.GetUpdateTime().AsTime().Equal(c.UpdateTime.AsTime()) {
			// This is the optimistic-concurrency check that makes a transaction
			// worth having: two consoles updating the same lobby document both
			// send the update_time they read, and the second one loses.
			return status.Error(codes.FailedPrecondition, "document was modified")
		}
	}
	return nil
}

// writeDocument merges the named fields of doc into the stored document.
//
// Called with the lock held. Creates the document if it is absent, which is what
// a client writing to a new path in a lobby expects -- there is no "create
// collection" step in this protocol.
func (s *store) writeDocument(doc *syncpb.Document, mask []string, now time.Time) *syncpb.Document {
	name := doc.GetName()
	stored, ok := s.docs[name]
	if !ok {
		stored = &syncpb.Document{
			Name:       name,
			Fields:     emptyMap(),
			CreateTime: timestamppb.New(now),
		}
		s.docs[name] = stored
	}
	if stored.Fields == nil {
		stored.Fields = emptyMap()
	}

	if len(mask) == 0 {
		// No mask means replace the whole field set. That is what an update
		// without a mask means everywhere this shape is used, and it is why
		// MergeDocument passes the sent field names as its mask instead.
		stored.Fields = emptyMap()
		for k, v := range doc.GetFields().GetFields() {
			stored.Fields.Fields[k] = proto.Clone(v).(*commonpb.Value)
		}
	} else {
		for _, path := range mask {
			// Top-level fields only, matching applyReadMask. A nested path
			// would need structural merging inside a MapValue, and getting that
			// subtly wrong loses a field rather than failing.
			field := strings.SplitN(path, ".", 2)[0]
			v, present := doc.GetFields().GetFields()[field]
			if !present {
				// A masked field the client did not send is a deletion. That is
				// what a field mask means, and treating it as "leave alone"
				// would make clearing a field impossible.
				delete(stored.Fields.Fields, field)
				continue
			}
			stored.Fields.Fields[field] = proto.Clone(v).(*commonpb.Value)
		}
	}

	stored.UpdateTime = timestamppb.New(now)
	return stored
}

// transform applies field transforms and returns the resulting values.
func (s *store) transform(
	req *syncpb.TransformDocumentRequest, now time.Time,
) (*syncpb.Document, []*commonpb.Value, error) {
	name := req.GetName()
	stored, ok := s.docs[name]
	if !ok {
		stored = &syncpb.Document{
			Name:       name,
			Fields:     emptyMap(),
			CreateTime: timestamppb.New(now),
		}
		s.docs[name] = stored
	}
	if stored.Fields == nil {
		stored.Fields = emptyMap()
	}

	values := make([]*commonpb.Value, 0, len(req.GetFieldTransforms()))
	for _, ft := range req.GetFieldTransforms() {
		field := strings.SplitN(ft.GetFieldPath(), ".", 2)[0]
		current := stored.Fields.Fields[field]

		var next *commonpb.Value
		switch t := ft.GetTransformType().(type) {
		case *syncpb.FieldTransform_SetServerValue:
			// The one server value that means anything here: the commit time.
			// Anything else is a value only the server knows, and there is
			// nothing else this server knows that a client would ask for.
			next = &commonpb.Value{
				ValueType: &commonpb.Value_TimestampValue{TimestampValue: timestamppb.New(now)},
			}
		case *syncpb.FieldTransform_Increment:
			next = arithmetic(current, t.Increment, func(a, b float64) float64 { return a + b })
		case *syncpb.FieldTransform_Maximum:
			next = arithmetic(current, t.Maximum, func(a, b float64) float64 {
				if a > b {
					return a
				}
				return b
			})
		case *syncpb.FieldTransform_Minimum:
			next = arithmetic(current, t.Minimum, func(a, b float64) float64 {
				if a < b {
					return a
				}
				return b
			})
		default:
			return nil, nil, status.Errorf(codes.Unimplemented,
				"field transform %T is not implemented", ft.GetTransformType())
		}

		stored.Fields.Fields[field] = next
		values = append(values, proto.Clone(next).(*commonpb.Value))
	}

	stored.UpdateTime = timestamppb.New(now)
	return stored, values, nil
}

// transformSupported reports whether a transform can be applied.
//
// The global-value transforms are not: they read and write a namespace shared
// across documents that nothing here models, and treating them as no-ops would
// have a client store a value it later reads back as absent.
func transformSupported(ft *syncpb.FieldTransform) bool {
	switch ft.GetTransformType().(type) {
	case *syncpb.FieldTransform_SetServerValue,
		*syncpb.FieldTransform_Increment,
		*syncpb.FieldTransform_Maximum,
		*syncpb.FieldTransform_Minimum:
		return true
	}
	return false
}

// arithmetic combines the stored value and the operand.
//
// Integers stay integers. A client that increments an integer field and reads
// back a double has had its type changed underneath it, and the value it writes
// next may not fit the field it came from.
func arithmetic(current, operand *commonpb.Value, combine func(a, b float64) float64) *commonpb.Value {
	a, aIsInt := numeric(current)
	b, bIsInt := numeric(operand)

	result := combine(a, b)
	if aIsInt && bIsInt {
		return &commonpb.Value{
			ValueType: &commonpb.Value_IntegerValue{IntegerValue: int64(result)},
		}
	}
	return &commonpb.Value{
		ValueType: &commonpb.Value_DoubleValue{DoubleValue: result},
	}
}

// numeric reads a value as a number, reporting whether it was an integer.
//
// A missing or non-numeric value reads as integer zero, so incrementing a field
// that does not exist yet creates it — which is the only useful reading, since
// there is no separate "create field" step.
func numeric(v *commonpb.Value) (float64, bool) {
	switch t := v.GetValueType().(type) {
	case *commonpb.Value_IntegerValue:
		return float64(t.IntegerValue), true
	case *commonpb.Value_DoubleValue:
		return t.DoubleValue, false
	case *commonpb.Value_FloatValue:
		return float64(t.FloatValue), false
	default:
		return 0, true
	}
}

func fieldNames(doc *syncpb.Document) []string {
	fields := doc.GetFields().GetFields()
	if len(fields) == 0 {
		// An empty merge must not become a maskless write, which would clear
		// every field the document has.
		return []string{""}
	}
	return sortedNames(fields)
}

func updateResult() *syncpb.WriteResult {
	return &syncpb.WriteResult{
		ResultType: &syncpb.WriteResult_UpdateResult{UpdateResult: &syncpb.UpdateResult{}},
	}
}

// changeFor renders a document change for the watch stream.
//
// The document is cloned: the change goes to every watcher, and they must not be
// able to see it mutate under them when the next write lands.
func changeFor(doc *syncpb.Document, kind syncpb.DocumentChange_DocumentChangeType) *syncpb.DocumentChange {
	return &syncpb.DocumentChange{
		DocumentChangeType: kind,
		Document:           proto.Clone(doc).(*syncpb.Document),
	}
}
