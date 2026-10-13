package splat3save

import (
	"fmt"
	"math"
)

// node is one parsed MessagePack value. Only the types a save has been seen
// to carry are kept; anything else (nil, bin, ext) is a parse error, because
// a type nobody has seen is one nobody knows how the cloud record spells.
type node struct {
	kind  kind
	i     int64 // kindInt
	u     uint64
	f     float64
	s     string
	b     bool
	keys  []node // kindMap
	elems []node // kindMap values, kindArray elements
}

type kind int

const (
	kindInt kind = iota // negative, or from a signed encoding
	kindUint
	kindFloat
	kindString
	kindBool
	kindMap
	kindArray
)

// maxDepth bounds recursion on a hostile upload; real saves nest about 6.
const maxDepth = 64

type reader struct {
	b []byte
	i int
}

func (r *reader) take(n int) ([]byte, error) {
	if n < 0 || len(r.b)-r.i < n {
		return nil, fmt.Errorf("splat3save: truncated at byte %d", r.i)
	}
	p := r.b[r.i : r.i+n]
	r.i += n
	return p, nil
}

func (r *reader) uintN(n int) (uint64, error) {
	p, err := r.take(n)
	if err != nil {
		return 0, err
	}
	var v uint64
	for _, c := range p {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

func (r *reader) value(depth int) (node, error) {
	if depth > maxDepth {
		return node{}, fmt.Errorf("splat3save: nested deeper than %d", maxDepth)
	}
	at := r.i
	p, err := r.take(1)
	if err != nil {
		return node{}, err
	}
	t := p[0]
	var n uint64
	switch {
	case t <= 0x7f:
		return node{kind: kindUint, u: uint64(t)}, nil
	case t >= 0xe0:
		return node{kind: kindInt, i: int64(int8(t))}, nil
	case t&0xf0 == 0x80:
		return r.mapBody(int(t&0x0f), depth)
	case t&0xf0 == 0x90:
		return r.arrayBody(int(t&0x0f), depth)
	case t&0xe0 == 0xa0:
		return r.str(int(t & 0x1f))
	}
	switch t {
	case 0xc2, 0xc3:
		return node{kind: kindBool, b: t == 0xc3}, nil
	case 0xcc, 0xcd, 0xce, 0xcf:
		v, err := r.uintN(1 << (t - 0xcc))
		return node{kind: kindUint, u: v}, err
	case 0xd0, 0xd1, 0xd2, 0xd3:
		w := 1 << (t - 0xd0)
		v, err := r.uintN(w)
		shift := 64 - 8*w
		return node{kind: kindInt, i: int64(v<<shift) >> shift}, err
	case 0xca:
		v, err := r.uintN(4)
		return node{kind: kindFloat, f: float64(math.Float32frombits(uint32(v)))}, err
	case 0xcb:
		v, err := r.uintN(8)
		return node{kind: kindFloat, f: math.Float64frombits(v)}, err
	case 0xd9, 0xda, 0xdb:
		if n, err = r.uintN(1 << (t - 0xd9)); err != nil {
			return node{}, err
		}
		return r.str(int(n))
	case 0xdc, 0xdd:
		if n, err = r.uintN(2 << (t - 0xdc)); err != nil {
			return node{}, err
		}
		return r.arrayBody(int(n), depth)
	case 0xde, 0xdf:
		if n, err = r.uintN(2 << (t - 0xde)); err != nil {
			return node{}, err
		}
		return r.mapBody(int(n), depth)
	}
	return node{}, fmt.Errorf("splat3save: unexpected MessagePack type %#02x at byte %d", t, at)
}

func (r *reader) str(n int) (node, error) {
	p, err := r.take(n)
	return node{kind: kindString, s: string(p)}, err
}

func (r *reader) mapBody(n, depth int) (node, error) {
	// Every entry is at least two bytes; refuse a count the input cannot hold
	// before allocating for it.
	if n > (len(r.b)-r.i)/2 {
		return node{}, fmt.Errorf("splat3save: map of %d entries at byte %d is truncated", n, r.i)
	}
	m := node{kind: kindMap, keys: make([]node, n), elems: make([]node, n)}
	for j := 0; j < n; j++ {
		var err error
		if m.keys[j], err = r.value(depth + 1); err != nil {
			return node{}, err
		}
		if m.elems[j], err = r.value(depth + 1); err != nil {
			return node{}, err
		}
	}
	return m, nil
}

func (r *reader) arrayBody(n, depth int) (node, error) {
	if n > len(r.b)-r.i {
		return node{}, fmt.Errorf("splat3save: array of %d elements at byte %d is truncated", n, r.i)
	}
	a := node{kind: kindArray, elems: make([]node, n)}
	for j := 0; j < n; j++ {
		var err error
		if a.elems[j], err = r.value(depth + 1); err != nil {
			return node{}, err
		}
	}
	return a, nil
}
