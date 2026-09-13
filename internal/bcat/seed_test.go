package bcat

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheSummaryMatchesARealOne, field for field and in order.
//
// Compared against the summary.yml from a seed folder a working server produced
// (JUEA-00210, 2026-08-24). The console parses this to learn which fest its
// cache holds, and the announced schedule has to agree with all three values or
// the game answers BcatInvalid.
func TestTheSummaryMatchesARealOne(t *testing.T) {
	s := Seed{
		FestID:     "JUEA-00210",
		ResourceID: "juea-lovefes2",
		Revision:   "a587beaaaf515afda77d35901c62038c8484a042",
	}
	want := "FestID: JUEA-00210\n" +
		"FestResourceID: juea-lovefes2\n" +
		"FestResourceRevision: a587beaaaf515afda77d35901c62038c8484a042\n"

	if got := s.Summary(); got != want {
		t.Errorf("summary =\n%q\nwant\n%q", got, want)
	}
}

func TestASummaryRoundTripsThroughReadSummary(t *testing.T) {
	s := Seed{
		FestID:     "NXTD-00042",
		ResourceID: "nxtd-lovefes",
		Revision:   strings.Repeat("a", 40),
	}
	got, err := ReadSummary([]byte(s.Summary()))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.FestID != s.FestID || got.ResourceID != s.ResourceID || got.Revision != s.Revision {
		t.Errorf("round trip gave %+v, want %+v", got, s)
	}
}

func TestReadSummaryParsesTheRealFileVerbatim(t *testing.T) {
	// Byte for byte as it appears on disk in a real seed.
	raw := []byte("FestID: JUEA-00210\nFestResourceID: juea-lovefes2\n" +
		"FestResourceRevision: a587beaaaf515afda77d35901c62038c8484a042\n")

	got, err := ReadSummary(raw)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.FestID != "JUEA-00210" {
		t.Errorf("FestID = %q", got.FestID)
	}
	if got.ResourceID != "juea-lovefes2" {
		t.Errorf("FestResourceID = %q", got.ResourceID)
	}
	if got.Revision != "a587beaaaf515afda77d35901c62038c8484a042" {
		t.Errorf("FestResourceRevision = %q", got.Revision)
	}
}

func TestAnIncompleteSummaryIsRefused(t *testing.T) {
	if _, err := ReadSummary([]byte("FestID: JUEA-00210\n")); err == nil {
		t.Error("a summary missing its resource and revision was accepted")
	}
}

func TestASeedThatWouldNotLoadIsRefused(t *testing.T) {
	good := Seed{
		FestID: "NXTD-00042", ResourceID: "nxtd-lovefes",
		Revision: strings.Repeat("a", 40), Regions: []string{"us"},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("a valid seed was refused: %v", err)
	}

	lowerID := good
	lowerID.FestID = "nxtd-00042"
	if err := lowerID.Validate(); err == nil {
		t.Error("a lowercase fest id was accepted; the console matches it uppercase")
	}

	upperResource := good
	upperResource.ResourceID = "NXTD-Lovefes"
	if err := upperResource.Validate(); err == nil {
		t.Error("a capitalised resource was accepted; every pack's IV derives from it")
	}

	noRegions := good
	noRegions.Regions = nil
	if err := noRegions.Validate(); err == nil {
		t.Error("a seed for no regions was accepted")
	}
}

// TestASeedAndItsKeysRoundTrip is the contract `switchnetctl fest build` exists
// to guarantee: the four values that must agree -- fest id, resource name,
// revision and pack keys -- come out of one build and still agree on disk.
//
// It builds packs from an assets directory, writes the seed, and then does what
// a console does: takes the key GetFestDecryptionKey would hand over, derives
// the IV from the announced resource name, decrypts, and parses the archive. Any
// disagreement between the two halves shows up here as a pack that does not
// open, which on a console is a generic communication error naming nothing.
func TestASeedAndItsKeysRoundTrip(t *testing.T) {
	assets := t.TempDir()
	for _, f := range []struct{ path, body string }{
		{"base/Mals/Msg_EUen.sarc.zs", "the fest's main content"},
		{"base/RSDB/Tag.dev.rstbl.byml.zs", "tables"},
		{"bh/help.byml", "battle help"},
		{"wa/team.byml", "alpha"},
	} {
		full := filepath.Join(assets, f.path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(f.body), 0o644); err != nil {
			t.Fatalf("write asset: %v", err)
		}
	}

	packs, err := BuildPacks(assets)
	if err != nil {
		t.Fatalf("build packs: %v", err)
	}
	// wb and wc were not supplied, so they must not appear: a fest need not
	// have every pack, and inventing an empty one would give the console a pack
	// with nothing in it rather than no pack.
	if len(packs) != 3 {
		t.Fatalf("built %d packs, want 3 (base, bh, wa)", len(packs))
	}

	// The revision comes from the packs' own bytes, which is what makes it
	// impossible for the announced revision and the pack contents to disagree.
	revision := Revision(packs)
	if len(revision) != 40 {
		t.Fatalf("revision is %q, want 40 hex characters", revision)
	}

	keys := map[string][]byte{}
	for name := range packs {
		key := make([]byte, 16)
		// Distinct per pack, so a key used for the wrong pack fails rather than
		// coincidentally working.
		for i := range key {
			key[i] = name[0] ^ byte(i)
		}
		keys[name] = key
	}

	seed := Seed{
		FestID:     "NXTD-00042",
		ResourceID: "nxtd-lovefes2",
		Revision:   revision,
		Regions:    []string{"EU", "JP"},
	}
	out := t.TempDir()
	if err := seed.WriteWith(out, packs, keys, testTable); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	// What the console reads first: which fest its cache holds.
	summaryRaw, err := os.ReadFile(filepath.Join(out, "bcat-seed", "eu-default", "summary.yml"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	got, err := ReadSummary(summaryRaw)
	if err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	if got.FestID != seed.FestID || got.ResourceID != seed.ResourceID || got.Revision != revision {
		t.Errorf("summary says %+v, want %+v", got, seed)
	}

	// And then, per pack, exactly what a console does with the key the schedule
	// handed it.
	for name, want := range packs {
		encrypted, err := os.ReadFile(
			filepath.Join(out, "bcat-seed", "eu-default", name+".pack.zs.enc"))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		plain, err := DecryptWith(got.ResourceID, name, keys[name], encrypted, testTable)
		if err != nil {
			t.Fatalf("decrypt %s with the key the fest row holds: %v", name, err)
		}
		if !bytes.Equal(plain, want) {
			t.Errorf("%s did not round-trip: %d bytes out, %d in", name, len(plain), len(want))
		}
		if _, err := ParseSARC(plain); err != nil {
			t.Errorf("%s is not a readable archive after the round trip: %v", name, err)
		}
	}

	// The base pack's IV path is the resource alone and every other pack's
	// appends its short name. Using base's key under another pack's path must
	// fail, or that asymmetry is not actually being applied.
	encrypted, err := os.ReadFile(filepath.Join(out, "bcat-seed", "eu-default", "base.pack.zs.enc"))
	if err != nil {
		t.Fatalf("read base: %v", err)
	}
	if plain, err := DecryptWith(got.ResourceID, PackBattleHelp, keys[PackBase], encrypted, testTable); err == nil {
		if _, err := ParseSARC(plain); err == nil {
			t.Error("base decrypted under bh's IV path, so the naming asymmetry is not applied")
		}
	}
}
