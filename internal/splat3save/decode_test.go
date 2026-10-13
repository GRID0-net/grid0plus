package splat3save

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/n-popescu/grid0plus/proto/common"
)

// fakeTable stands in for the game's table, which is never in the
// repository. The scheme does not care what the words are.
func fakeTable() *Table {
	var t Table
	for i := range t {
		t[i] = uint32(i)*0x9e3779b9 + 7
	}
	return &t
}

// mp builds MessagePack the way the game writes it: keys as 0xce u32s.
type mp []byte

func (m mp) u32(v uint32) mp {
	return binary.BigEndian.AppendUint32(append(m, 0xce), v)
}
func (m mp) key(name string) mp { return m.u32(murmur3([]byte(name), 0)) }
func (m mp) fix(v byte) mp      { return append(m, v) }
func (m mp) mapN(n byte) mp     { return append(m, 0x80|n) }
func (m mp) arrN(n byte) mp     { return append(m, 0x90|n) }
func (m mp) str(s string) mp    { return append(append(m, 0xa0|byte(len(s))), s...) }
func (m mp) f32(f float32) mp {
	return binary.BigEndian.AppendUint32(append(m, 0xca), math.Float32bits(f))
}

func save(server mp) []byte {
	plain := mp{'M', 'S', 0, 4, 0, 11, 3, 0}
	plain = plain.mapN(1).key("Common").mapN(0) // the local half, skipped
	plain = append(plain, server...)
	return Encrypt(plain, 0x1234abcd, fakeTable())
}

func TestASaveBecomesTheRecordWithNamesAndDecimalIds(t *testing.T) {
	server := mp{}.mapN(6).
		key("RewardAry").arrN(0).
		key("Money").u32(3000000000).
		key("UserName").str("Nico").
		key("HaveWeaponMap").mapN(1).fix(10).mapN(1).key("MaxWeaponPower").f32(1500.5).
		key("CloudRandomSet").mapN(1).key("Map").mapN(1).key("Vendor").mapN(0).
		key("Shop").mapN(1).key("IsReadWeaponExp").mapN(1).fix(40).append1(0xc3)

	rec, info, err := Decode(save(server), fakeTable())
	if err != nil {
		t.Fatal(err)
	}
	if info.GameVersion != "11.3.0" || info.Fields != 6 {
		t.Errorf("info = %+v", info)
	}
	f := rec.GetFields()
	if got := f["Money"].GetIntegerValue(); got != 3000000000 {
		t.Errorf("Money = %d, want the unsigned value", got)
	}
	if _, ok := f["RewardAry"].GetValueType().(*common.Value_NullValue); !ok {
		t.Errorf("an empty array is %T, want null as CloudSave stores it", f["RewardAry"].GetValueType())
	}
	if got := f["UserName"].GetStringValue(); got != "Nico" {
		t.Errorf("UserName = %q", got)
	}
	weapon := f["HaveWeaponMap"].GetMapValue().GetFields()["10"]
	if weapon == nil {
		t.Fatalf("HaveWeaponMap keys = %v, want the id spelled \"10\"", keysOf(f["HaveWeaponMap"].GetMapValue()))
	}
	if _, ok := weapon.GetMapValue().GetFields()["MaxWeaponPower"].GetValueType().(*common.Value_DoubleValue); !ok {
		t.Error("MaxWeaponPower is not a DoubleValue")
	}
	// An id under a ...Map stays a number even when it hashes to a known name.
	rs := f["CloudRandomSet"].GetMapValue().GetFields()["Map"].GetMapValue()
	want := strconv.FormatUint(uint64(murmur3([]byte("Vendor"), 0)), 10)
	if rs.GetFields()[want] == nil {
		t.Errorf("CloudRandomSet.Map keys = %v, want %s", keysOf(rs), want)
	}
	// An id map without the suffix is recognised by having no known names.
	if f["Shop"].GetMapValue().GetFields()["IsReadWeaponExp"].GetMapValue().GetFields()["40"] == nil {
		t.Error("Shop.IsReadWeaponExp did not keep its id 40")
	}
}

func TestAFieldWithAnUnknownNameRefusesTheImport(t *testing.T) {
	server := mp{}.mapN(2).key("Money").fix(1).key("NotAFieldTheListKnows").fix(2)
	_, _, err := Decode(save(server), fakeTable())
	if err == nil || !strings.Contains(err.Error(), "unknown names") {
		t.Fatalf("err = %v, want a refusal naming the unknown field", err)
	}
}

func TestASaveFromAnotherTableIsACheckFailure(t *testing.T) {
	file := save(mp{}.mapN(0))
	other := fakeTable()
	for i := range other {
		other[i]++
	}
	if _, _, err := Decode(file, other); !errors.Is(err, ErrCheck) {
		t.Fatalf("err = %v, want ErrCheck", err)
	}
	file[len(file)-1] ^= 1
	if _, _, err := Decode(file, fakeTable()); !errors.Is(err, ErrCheck) {
		t.Fatalf("damaged file: err = %v, want ErrCheck", err)
	}
}

func TestTrailingBytesAndUnknownTypesAreRefused(t *testing.T) {
	if _, _, err := Decode(save(mp{}.mapN(0).fix(1)), fakeTable()); err == nil {
		t.Error("bytes after the server half were accepted")
	}
	if _, _, err := Decode(save(mp{}.mapN(1).key("Money").append1(0xc0)), fakeTable()); err == nil {
		t.Error("a nil value was accepted")
	}
	if _, _, err := Decode(save(mp{}.mapN(1).key("Money").arrN(15)), fakeTable()); err == nil {
		t.Error("a truncated array was accepted")
	}
}

// TestARealSaveDecodes runs only on a machine holding the game's table and a
// real save, which never enter the repository:
//
//	SPLAT3SAVE_TABLE=… SPLAT3SAVE_FILE=…/save.dat go test ./internal/splat3save
func TestARealSaveDecodes(t *testing.T) {
	tp, fp := os.Getenv("SPLAT3SAVE_TABLE"), os.Getenv("SPLAT3SAVE_FILE")
	if tp == "" || fp == "" {
		t.Skip("SPLAT3SAVE_TABLE and SPLAT3SAVE_FILE not set")
	}
	table, err := LoadTable(tp)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	rec, info, err := Decode(file, table)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("version %s, %d fields", info.GameVersion, info.Fields)
	for _, k := range []string{"Money", "PlayerRank", "UserName", "HaveWeaponMap"} {
		if rec.GetFields()[k] == nil {
			t.Errorf("no %s", k)
		}
	}
}

func (m mp) append1(b byte) mp { return append(m, b) }

func keysOf(m *common.MapValue) []string {
	var ks []string
	for k := range m.GetFields() {
		ks = append(ks, k)
	}
	return ks
}
