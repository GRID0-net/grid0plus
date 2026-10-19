// Package splat3save turns a Splatoon 3 save.dat into the cloud save record
// GRID0+'s CloudSave serves (toyohr's saveRecords), so a player can bring an
// existing save to the server.
package splat3save

import (
	"bufio"
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/n-popescu/grid0plus/proto/common"
)

// ErrCheck is a file whose check word does not match: not a Splatoon 3 save,
// a damaged one, or one from a game version whose table differs.
var ErrCheck = errors.New("splat3save: check word does not match; not a save this table opens")

// Info describes a decoded save.
type Info struct {
	// GameVersion is the version the save was written by, from its header
	// (e.g. "11.3.0").
	GameVersion string
	// Fields is the number of top-level fields in the server record.
	Fields int
	// Unknown lists every field left out of the record because its name is
	// not known, as "path: hash". The record is everything else; the caller
	// keeps the original save for these.
	Unknown []string
}

// names are the save's field names. The save stores murmur3_32(name, 0)
// instead of the name; the cloud record spells it out. The list is every
// field seen in a decrypted 11.3.0 save plus every name in a stored record,
// hashed back from the game's own strings. These are field names, not key
// material.
//
//go:embed names.txt
var namesTxt string

var byHash = func() map[uint32]string {
	m := map[uint32]string{}
	sc := bufio.NewScanner(strings.NewReader(namesTxt))
	for sc.Scan() {
		n := strings.TrimSpace(sc.Text())
		if n == "" {
			continue
		}
		h := murmur3([]byte(n), 0)
		if prev, ok := m[h]; ok && prev != n {
			panic("splat3save: " + prev + " and " + n + " hash alike")
		}
		m[h] = n
	}
	return m
}()

// LoadNames adds field names from an operator file, one per line: a list
// taken from the game's own strings resolves fields newer than names.txt
// without a release. Names are not key material, but a list cut from a game
// dump stays with the operator, like the key table. Call it before decoding.
func LoadNames(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	added := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		n := strings.TrimSpace(sc.Text())
		if n == "" {
			continue
		}
		h := murmur3([]byte(n), 0)
		// A big list would otherwise make some small game id (a weapon's 40)
		// look like a name and turn an id map into a struct. Ids stay below
		// 2^24 (hashLike); a name hashing there must come from names.txt.
		if h < 1<<24 {
			continue
		}
		if _, ok := byHash[h]; ok {
			// names.txt wins: two strings can share a hash, and the
			// embedded list is the one checked against stored records.
			continue
		}
		byHash[h] = n
		added++
	}
	return added, sc.Err()
}

// header is "MS", format version 4, which the game's loader checks first.
var header = []byte{'M', 'S', 0, 4}

// Decode decrypts a save.dat and returns the server half of it as a cloud
// save record.
//
// The plaintext is an 8-byte header and two MessagePack maps: the first is
// what stays on the console, the second is exactly what the game keeps in
// the cloud record (every path of a stored record exists in a decoded save
// with the same value type). Only the second is returned.
func Decode(file []byte, t *Table) (*common.MapValue, Info, error) {
	plain, err := decrypt(file, t)
	if err != nil {
		return nil, Info{}, err
	}
	if len(plain) < 8 || !bytes.Equal(plain[:4], header) {
		return nil, Info{}, errors.New("splat3save: decrypted data does not start with the MS v4 header")
	}
	info := Info{GameVersion: fmt.Sprintf("%d.%d.%d", plain[5], plain[6], plain[7])}

	r := &reader{b: plain, i: 8}
	if _, err := r.value(0); err != nil {
		return nil, info, fmt.Errorf("local half: %w", err)
	}
	server, err := r.value(0)
	if err != nil {
		return nil, info, fmt.Errorf("server half: %w", err)
	}
	if r.i != len(plain) {
		return nil, info, fmt.Errorf("splat3save: %d bytes after the server half", len(plain)-r.i)
	}
	if server.kind != kindMap {
		return nil, info, errors.New("splat3save: server half is not a map")
	}
	d := &decoder{}
	m, err := d.convertMap(server, "", false)
	if err != nil {
		return nil, info, err
	}
	info.Fields, info.Unknown = len(m.Fields), d.unknown
	return m, info, nil
}

// convertMap names a map's keys. A map is either a struct, whose keys are all
// hashed field names, or an id map (weapons, gear, badges, random sets),
// whose keys the record spells as decimal strings. Which one is decided per
// map, never per key:
//
//   - the value of a field named "...Map" is an id map, always. Its ids can
//     themselves be hashes of names (CloudRandomSet.Map keys hash to "Lot",
//     "Vendor") and the record still spells them as numbers;
//   - otherwise every key named makes a struct, no key named an id map
//     (Shop.IsReadWeaponExp is one without the suffix);
//   - a mix is refused. It means a field this list does not know, and the
//     import replaces the whole record, so writing that field under a number
//     would lose it silently.
func (d *decoder) convertMap(n node, path string, idMap bool) (*common.MapValue, error) {
	out := &common.MapValue{Fields: make(map[string]*common.Value, len(n.keys))}
	keys := make([]string, len(n.keys))
	named := 0
	for i, k := range n.keys {
		var id string
		switch k.kind {
		case kindUint:
			id = strconv.FormatUint(k.u, 10)
		case kindInt:
			id = strconv.FormatInt(k.i, 10)
		default:
			return nil, fmt.Errorf("splat3save: %s has a non-integer key", pathOr(path))
		}
		keys[i] = id
		if idMap || k.kind != kindUint || k.u > 0xffffffff {
			continue
		}
		if name, ok := byHash[uint32(k.u)]; ok {
			keys[i] = name
			named++
		}
	}
	// A struct whose names are all unknown would otherwise pass for an id map
	// and be stored under numbers: ids are small, a name's hash almost never
	// is. Every key of such a map is an unknown field.
	if !idMap && named == 0 && len(keys) > 0 && hashLike(n.keys) {
		named = -1
	}
	if !idMap && (named < 0 || named > 0 && named < len(keys)) {
		// Left out and reported rather than refused: the caller keeps the
		// save itself, so nothing is lost, and the player still gets the
		// rest of their save.
		skip := map[int]bool{}
		for i, k := range n.keys {
			if k.kind != kindUint || k.u > 0xffffffff {
				continue
			}
			if _, ok := byHash[uint32(k.u)]; !ok {
				skip[i] = true
				d.unknown = append(d.unknown, fmt.Sprintf("%s: %#08x", pathOr(path), k.u))
			}
		}
		for i := range keys {
			if skip[i] {
				continue
			}
			v, err := d.convertValue(n.elems[i], path+"."+keys[i], idMapName(keys[i]))
			if err != nil {
				return nil, err
			}
			out.Fields[keys[i]] = v
		}
		return out, nil
	}
	if !idMap && named == 0 {
		// An id map: put back the decimal spelling of every key.
		for i, k := range n.keys {
			if k.kind == kindUint {
				keys[i] = strconv.FormatUint(k.u, 10)
			}
		}
	}
	for i, name := range keys {
		if _, dup := out.Fields[name]; dup {
			return nil, fmt.Errorf("splat3save: %s.%s appears twice", path, name)
		}
		v, err := d.convertValue(n.elems[i], path+"."+name, named > 0 && idMapName(name))
		if err != nil {
			return nil, err
		}
		out.Fields[name] = v
	}
	return out, nil
}

// convertValue keeps the save's MessagePack type.
//
// UNCONFIRMED: about 170 of the 551 path shapes a decoded save
// carries (most under Sdodr/Fsodr, MiniGame and the Shop histories) appear
// in none of the 13 records grid0 holds, so their record type is inferred
// from the wire type rather than seen. A record written by a console that
// has played those modes would settle it; a mismatch would be fixed here.
func (d *decoder) convertValue(n node, path string, idMap bool) (*common.Value, error) {
	switch n.kind {
	case kindUint:
		if n.u > 1<<63-1 {
			return nil, fmt.Errorf("splat3save: %s does not fit a record integer", path)
		}
		return &common.Value{ValueType: &common.Value_IntegerValue{IntegerValue: int64(n.u)}}, nil
	case kindInt:
		return &common.Value{ValueType: &common.Value_IntegerValue{IntegerValue: n.i}}, nil
	case kindFloat:
		// float32 in the save (MaxWeaponPower), DoubleValue in the record.
		return &common.Value{ValueType: &common.Value_DoubleValue{DoubleValue: n.f}}, nil
	case kindString:
		return &common.Value{ValueType: &common.Value_StringValue{StringValue: n.s}}, nil
	case kindBool:
		return &common.Value{ValueType: &common.Value_BooleanValue{BooleanValue: n.b}}, nil
	case kindMap:
		m, err := d.convertMap(n, path, idMap)
		if err != nil {
			return nil, err
		}
		return &common.Value{ValueType: &common.Value_MapValue{MapValue: m}}, nil
	case kindArray:
		// Stored as null, the way CloudSave stores a console's own empty
		// array (toyohr's storedSaveValue), so an imported record reads
		// back like one the game wrote.
		if len(n.elems) == 0 {
			return &common.Value{ValueType: &common.Value_NullValue{}}, nil
		}
		a := &common.ArrayValue{Values: make([]*common.Value, len(n.elems))}
		for i, e := range n.elems {
			v, err := d.convertValue(e, path+"[]", false)
			if err != nil {
				return nil, err
			}
			a.Values[i] = v
		}
		return &common.Value{ValueType: &common.Value_ArrayValue{ArrayValue: a}}, nil
	}
	return nil, fmt.Errorf("splat3save: %s has an unsupported value", path)
}

// idMapName says a field holds an id map: "...Map", or a mode's own copy of
// one, "...Map_<mode>" (PaletteRewardAcceptFlagMap_Sdodr, whose ids are
// themselves hashes).
func idMapName(name string) bool {
	return strings.HasSuffix(name, "Map") || strings.Contains(name, "Map_")
}

// decoder collects what a decode leaves out.
type decoder struct{ unknown []string }

// hashLike says a map's keys look like name hashes rather than ids: game ids
// (weapons, gear, badges) stay well under 2^24.
func hashLike(keys []node) bool {
	for _, k := range keys {
		if k.kind == kindUint && k.u >= 1<<24 && k.u <= 0xffffffff {
			return true
		}
	}
	return false
}

func pathOr(p string) string {
	if p == "" {
		return "the record"
	}
	return p
}
