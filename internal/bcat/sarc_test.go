package bcat

import (
	"bytes"
	"testing"
)

// Real packs cannot be committed — they are Nintendo's content. These use
// synthetic files through the identical code.
//
// The implementation was verified against three REAL Splatfest packs outside
// the test suite: each parsed, and each rebuilt byte-identically from its own
// parsed contents (14531, 29622 and 581688 bytes). That round trip is what
// caught the eight-byte data alignment below.

func TestASARCRoundTripsThroughParseAndBuild(t *testing.T) {
	files := []SARCFile{
		{Name: "Mals/Msg_USen.Fest_NXTD-00042_Bh.sarc.zs", Data: []byte("english")},
		{Name: "Mals/Msg_JPja.Fest_NXTD-00042_Bh.sarc.zs", Data: []byte("japanese")},
		{Name: "Event/EventFlow/Fest_NXTD-00042_BeingHeld.bfevfl", Data: bytes.Repeat([]byte{0xAB}, 300)},
	}

	raw, err := BuildSARC(files)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if string(raw[0:4]) != sarcMagic {
		t.Fatalf("no SARC magic: %q", raw[0:4])
	}

	got, err := ParseSARC(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != len(files) {
		t.Fatalf("parsed %d files, want %d", len(got), len(files))
	}

	byName := map[string][]byte{}
	for _, f := range got {
		byName[f.Name] = f.Data
	}
	for _, want := range files {
		data, ok := byName[want.Name]
		if !ok {
			t.Errorf("%q is missing after a round trip", want.Name)
			continue
		}
		if !bytes.Equal(data, want.Data) {
			t.Errorf("%q came back with %d bytes, want %d", want.Name, len(data), len(want.Data))
		}
	}
}

// TestTheFileDataStartsOnAnEightByteBoundary.
//
// Found by rebuilding three real packs: two landed on 8 anyway and matched
// under a 4-byte assumption, while the third needed the extra four. A 4-byte
// assumption is therefore right most of the time and silently shifts every file
// offset the rest of the time — the worst shape a bug can have.
func TestTheFileDataStartsOnAnEightByteBoundary(t *testing.T) {
	// Name lengths chosen so the table lands at 4 mod 8, which is the case that
	// distinguishes the two alignments.
	for _, nameLen := range []int{3, 7, 11, 15, 19, 23} {
		name := string(bytes.Repeat([]byte("a"), nameLen))
		raw, err := BuildSARC([]SARCFile{{Name: name, Data: []byte("x")}})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		dataOffset := int(raw[12]) | int(raw[13])<<8 | int(raw[14])<<16 | int(raw[15])<<24
		if dataOffset%8 != 0 {
			t.Errorf("name length %d gave data offset %d, which is not 8-aligned", nameLen, dataOffset)
		}
		if _, err := ParseSARC(raw); err != nil {
			t.Errorf("name length %d: our own archive does not parse: %v", nameLen, err)
		}
	}
}

// TestFilesAreOrderedByNameHash.
//
// The console binary-searches the node table by hash. An archive in insertion
// order does not fail — it reports most files as missing, which surfaces much
// later and looks nothing like an ordering bug.
func TestFilesAreOrderedByNameHash(t *testing.T) {
	raw, err := BuildSARC([]SARCFile{
		{Name: "zzz.bin", Data: []byte("z")},
		{Name: "aaa.bin", Data: []byte("a")},
		{Name: "mmm.bin", Data: []byte("m")},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	files, err := ParseSARC(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var prev uint32
	for i, f := range files {
		h := sarcNameHash(f.Name, sarcHashKey)
		if i > 0 && h < prev {
			t.Errorf("file %d (%q) hashes below its predecessor", i, f.Name)
		}
		prev = h
	}
}

func TestBuildingAnArchiveIsDeterministic(t *testing.T) {
	files := []SARCFile{
		{Name: "b.bin", Data: []byte("b")},
		{Name: "a.bin", Data: []byte("a")},
	}
	first, err := BuildSARC(files)
	if err != nil {
		t.Fatal(err)
	}
	// Reversed input must produce identical bytes: the pack's revision digest
	// is taken over these bytes, and a digest that moves when nothing changed
	// makes the console re-download a pack it already has.
	second, err := BuildSARC([]SARCFile{files[1], files[0]})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("the same files in a different order produced different archives")
	}
}

func TestAMalformedArchiveIsRefused(t *testing.T) {
	for name, input := range map[string][]byte{
		"empty":       {},
		"wrong magic": append([]byte("XARC"), make([]byte, 64)...),
	} {
		if _, err := ParseSARC(input); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	if _, err := BuildSARC(nil); err == nil {
		t.Error("an empty archive was built")
	}
	if _, err := BuildSARC([]SARCFile{{Name: "a", Data: nil}, {Name: "a", Data: nil}}); err == nil {
		t.Error("a duplicate filename was accepted")
	}
	if _, err := BuildSARC([]SARCFile{{Name: "", Data: nil}}); err == nil {
		t.Error("a file with no name was accepted")
	}
}
