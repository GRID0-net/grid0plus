package bcat

import (
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
