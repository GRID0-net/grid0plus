package ugcstore

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	ugcpb "github.com/n-popescu/switchnet/proto/ugcstore/v1"
)

// decodeFields reads a stored MapValue. An empty blob is an empty map.
func decodeFields(b []byte) (*commonpb.MapValue, error) {
	m := &commonpb.MapValue{}
	if err := proto.Unmarshal(b, m); err != nil {
		return nil, err
	}
	if m.Fields == nil {
		m.Fields = map[string]*commonpb.Value{}
	}
	return m, nil
}

func encodeFields(m *commonpb.MapValue) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(m)
}

// splitPath splits a dotted field path ("a.b.c").
func splitPath(p string) ([]string, error) {
	if p == "" {
		return nil, fmt.Errorf("empty field path")
	}
	parts := strings.Split(p, ".")
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("bad field path %q", p)
		}
	}
	return parts, nil
}

// getPath returns the value at a dotted path, or nil.
func getPath(m *commonpb.MapValue, parts []string) *commonpb.Value {
	for i, part := range parts {
		v := m.GetFields()[part]
		if i == len(parts)-1 || v == nil {
			return v
		}
		next := v.GetMapValue()
		if next == nil {
			return nil
		}
		m = next
	}
	return nil
}

// setPath stores v at a dotted path, creating intermediate maps; a nil v
// removes the key.
func setPath(m *commonpb.MapValue, parts []string, v *commonpb.Value) {
	for i, part := range parts {
		if m.Fields == nil {
			m.Fields = map[string]*commonpb.Value{}
		}
		if i == len(parts)-1 {
			if v == nil {
				delete(m.Fields, part)
			} else {
				m.Fields[part] = v
			}
			return
		}
		next := m.Fields[part].GetMapValue()
		if next == nil {
			if v == nil {
				return
			}
			next = &commonpb.MapValue{Fields: map[string]*commonpb.Value{}}
			m.Fields[part] = &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: next}}
		}
		m = next
	}
}

// applyTransform runs one field transform against m and returns the value the
// field ends up holding, which is what WriteResult.transform_results reports.
func applyTransform(m *commonpb.MapValue, t *ugcpb.FieldTransform, now time.Time) (*commonpb.Value, error) {
	parts, err := splitPath(t.GetFieldPath())
	if err != nil {
		return nil, err
	}
	cur := getPath(m, parts)

	var out *commonpb.Value
	switch op := t.GetTransformType().(type) {
	case *ugcpb.FieldTransform_SetServerValue:
		if op.SetServerValue != ugcpb.ServerValueType_REQUEST_TIME {
			return nil, fmt.Errorf("unsupported server value %v", op.SetServerValue)
		}
		out = &commonpb.Value{ValueType: &commonpb.Value_TimestampValue{TimestampValue: timestamppb.New(now)}}
	case *ugcpb.FieldTransform_Increment:
		out, err = numeric(cur, op.Increment, func(a, b float64) float64 { return a + b },
			func(a, b int64) int64 { return a + b })
	case *ugcpb.FieldTransform_BufferedIncrement:
		out, err = numeric(cur, op.BufferedIncrement, func(a, b float64) float64 { return a + b },
			func(a, b int64) int64 { return a + b })
	case *ugcpb.FieldTransform_Maximum:
		out, err = numeric(cur, op.Maximum, maxF, maxI)
	case *ugcpb.FieldTransform_Minimum:
		out, err = numeric(cur, op.Minimum, minF, minI)
	default:
		return nil, fmt.Errorf("unsupported transform %T", op)
	}
	if err != nil {
		return nil, fmt.Errorf("field %q: %w", t.GetFieldPath(), err)
	}
	setPath(m, parts, out)
	return out, nil
}

// numeric combines the field's current value with an operand. A missing or
// non-numeric current value counts as absent, and the operand is the result
// (Firestore's rule, which this API mirrors). Two integers stay an integer;
// anything involving a double is a double.
func numeric(cur, operand *commonpb.Value, f func(a, b float64) float64, i func(a, b int64) int64) (*commonpb.Value, error) {
	ob, oInt, ok := number(operand)
	if !ok {
		return nil, fmt.Errorf("operand is not a number")
	}
	cb, cInt, ok := number(cur)
	if !ok {
		return proto.Clone(operand).(*commonpb.Value), nil
	}
	if cInt && oInt {
		return intValue(i(cur.GetIntegerValue(), operand.GetIntegerValue())), nil
	}
	return &commonpb.Value{ValueType: &commonpb.Value_DoubleValue{DoubleValue: f(cb, ob)}}, nil
}

func number(v *commonpb.Value) (f float64, isInt, ok bool) {
	switch x := v.GetValueType().(type) {
	case *commonpb.Value_IntegerValue:
		return float64(x.IntegerValue), true, true
	case *commonpb.Value_DoubleValue:
		return x.DoubleValue, false, true
	case *commonpb.Value_FloatValue:
		return float64(x.FloatValue), false, true
	}
	return 0, false, false
}

func intValue(n int64) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: n}}
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func maxI(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func minI(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
