package toyohr

import (
	"strings"
	"testing"
	"time"
)

// TestEveryPhaseLandsOnATwoHourSlot is the most important test in this package.
//
// Splatoon 3 files scheduled things into two-hour slots. A fest whose phases do
// not land on a slot boundary belongs to no slot, and the game stops treating it
// as open — it demands the fest's verdict instead, which a real console never
// does mid-fest. Every player's lobby then fails at once, and nothing anywhere
// says why.
//
// The offsets are all multiples of the slot length, so aligning the start is
// meant to align the whole table. This asserts that it actually does, from a
// start time deliberately chosen to be as misaligned as possible.
func TestEveryPhaseLandsOnATwoHourSlot(t *testing.T) {
	// 95 seconds past the hour, on an odd hour: wrong on every axis.
	ragged := time.Date(2026, 9, 5, 13, 1, 35, 0, time.UTC)
	f := NewFest(ragged, nil)

	tt := f.Timetable()
	for _, phase := range []struct {
		name string
		at   time.Time
	}{
		{"open", tt.Open},
		{"start", tt.Start},
		{"mid", tt.Mid},
		{"end", tt.End},
		{"close", tt.Close},
	} {
		if !IsSlotAligned(phase.at) {
			t.Errorf("%s is at %s, which is %d seconds past a two-hour slot boundary",
				phase.name, phase.at.Format(time.RFC3339), phase.at.Unix()%slotSeconds)
		}
	}
}

// TestThePhaseOffsetsMatchWhatTheGameExpects pins the intervals themselves.
//
// These are not ours to choose. A fest whose phases are spaced differently puts
// the game in a state it did not expect, and the offsets are the sort of
// constant that looks arbitrary and gets "tidied" by someone later.
func TestThePhaseOffsetsMatchWhatTheGameExpects(t *testing.T) {
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	tt := NewFest(start, nil).Timetable()

	for _, c := range []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"announced before start", tt.Start.Sub(tt.Open), 662400 * time.Second},
		{"midpoint after start", tt.Mid.Sub(tt.Start), 24 * time.Hour},
		{"end after start", tt.End.Sub(tt.Start), 48 * time.Hour},
		{"close after end", tt.Close.Sub(tt.End), 2 * time.Hour},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestTheDefaultStartIsTheNextSaturdayAndIsAligned(t *testing.T) {
	// A Wednesday afternoon, deliberately misaligned.
	now := time.Date(2026, 9, 2, 15, 47, 12, 0, time.UTC)
	start := NextFestStart(now)

	if start.Weekday() != time.Saturday {
		t.Errorf("start is a %s, want Saturday", start.Weekday())
	}
	if !start.After(now) {
		t.Errorf("start %s is not after %s", start, now)
	}
	if !IsSlotAligned(start) {
		t.Errorf("start %s is not slot-aligned", start)
	}
}

// TestAMisalignedStartIsRefusedRatherThanQuietlyMoved.
//
// NewFest aligns, because a caller asking for "next Saturday" wants a fest.
// Validate refuses, because a fest being loaded with a misaligned start is a
// fest that will fail on hardware, and quietly moving it would hide that from
// the operator who set the time.
func TestAMisalignedStartIsRefusedRatherThanQuietlyMoved(t *testing.T) {
	f := NewFest(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), nil)
	f.Start = f.Start.Add(90 * time.Second)

	err := f.Validate()
	if err == nil {
		t.Fatal("a fest starting off a slot boundary was accepted")
	}
	if !strings.Contains(err.Error(), "two-hour slot") {
		t.Errorf("the error does not explain the slot rule: %v", err)
	}
}

// TestTheResourceNameIsRefusedIfItIsNotLowercase.
//
// The console derives the pack's AES decryption IV from the resource name, so a
// single capital changes the IV and the pack never opens. The failure appears in
// the lobby as a generic communication error with nothing pointing at the name,
// which is why it is refused here instead.
func TestTheResourceNameIsRefusedIfItIsNotLowercase(t *testing.T) {
	f := NewFest(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), nil)
	f.Resource = "NXTD-00042_Data"

	err := f.Validate()
	if err == nil {
		t.Fatal("a resource name with capitals was accepted")
	}
	if !strings.Contains(err.Error(), "lowercase") {
		t.Errorf("the error does not explain why case matters: %v", err)
	}
}

// TestTheFestIDIsUppercaseAndTheResourceIsLowercase pins the two opposite
// rules together, because they are easy to conflate and they fail differently:
// a lowercase id is a different fest to the game, a capitalised resource is a
// pack that will not decrypt.
func TestTheFestIDIsUppercaseAndTheResourceIsLowercase(t *testing.T) {
	f := NewFest(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), nil)

	if f.ID != strings.ToUpper(f.ID) {
		t.Errorf("fest id %q is not uppercase", f.ID)
	}
	if f.Resource != strings.ToLower(f.Resource) {
		t.Errorf("resource %q is not lowercase", f.Resource)
	}
	if err := f.Validate(); err != nil {
		t.Errorf("a freshly built fest does not validate: %v", err)
	}
}

func TestTheRevisionIsFortyHexCharacters(t *testing.T) {
	f := NewFest(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), nil)
	if len(f.Revision) != 40 {
		t.Errorf("revision %q is %d characters, want 40", f.Revision, len(f.Revision))
	}

	// Distinct between fests: two fests sharing a revision would have the
	// console load one's pack for the other.
	other := NewFest(time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), nil)
	if f.Revision == other.Revision {
		t.Error("two different fests derived the same pack revision")
	}

	f.Revision = "not hexadecimal at all, but forty characters"[:40]
	if err := f.Validate(); err == nil {
		t.Error("a non-hex revision was accepted")
	}
}

func TestAFestWithNoRegionsIsRefused(t *testing.T) {
	f := NewFest(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), nil)
	f.Regions = nil
	if err := f.Validate(); err == nil {
		t.Error("a fest published in no regions was accepted; no console would ever see it")
	}
}

func TestThePhasesRunInOrderAcrossTheFest(t *testing.T) {
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	f := NewFest(start, nil)
	tt := f.Timetable()

	for _, c := range []struct {
		at   time.Time
		want Phase
	}{
		{tt.Open.Add(-time.Hour), PhaseNone},
		{tt.Open.Add(time.Hour), PhaseAnnounced},
		{tt.Start, PhaseFirstHalf},
		{tt.Mid.Add(-time.Second), PhaseFirstHalf},
		{tt.Mid, PhaseSecondHalf},
		{tt.End.Add(-time.Second), PhaseSecondHalf},
		{tt.End, PhaseCounting},
		{tt.Close.Add(-time.Second), PhaseCounting},
		{tt.Close, PhaseClosed},
	} {
		if got := f.PhaseAt(c.at); got != c.want {
			t.Errorf("at %s: phase = %s, want %s", c.at.Format(time.RFC3339), got, c.want)
		}
	}
}

// TestThePublicationWindowOpensBeforeTheAnnouncementAndOutlastsTheFest.
//
// A region's data has to be publishable before the fest is announced, or the
// console has nothing to fetch when the announcement arrives, and it has to stay
// published well past the close so a late console can still read the results.
func TestThePublicationWindowOpensBeforeTheAnnouncementAndOutlastsTheFest(t *testing.T) {
	f := NewFest(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), nil)
	tt := f.Timetable()
	start, end := f.PublishWindow()

	if !start.Before(tt.Open) {
		t.Errorf("publication starts at %s, which is not before the announcement at %s", start, tt.Open)
	}
	if got := tt.Open.Sub(start); got != 7*time.Hour {
		t.Errorf("publication opens %v before the announcement, want 7h", got)
	}
	if !end.After(tt.Close) {
		t.Errorf("publication ends at %s, before the fest closes at %s", end, tt.Close)
	}
}

// TestAnOperatorEndLengthensTheSecondHalfAndMovesTheResults pins the per-fest
// end of play: results still follow it by two hours, and an end that would
// put the timetable out of order is ignored rather than served.
func TestAnOperatorEndLengthensTheSecondHalfAndMovesTheResults(t *testing.T) {
	start := time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 13, 0, 30, 0, 0, time.UTC) // aligns down to 00:00

	tt := Fest{Start: start, End: end}.Timetable()
	if want := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC); !tt.Mid.Equal(want) {
		t.Errorf("mid = %s, want %s unchanged", tt.Mid, want)
	}
	if want := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC); !tt.End.Equal(want) {
		t.Errorf("end = %s, want %s", tt.End, want)
	}
	if want := time.Date(2026, 10, 13, 2, 0, 0, 0, time.UTC); !tt.Close.Equal(want) {
		t.Errorf("close = %s, want %s", tt.Close, want)
	}

	early := Fest{Start: start, End: start.Add(12 * time.Hour)}.Timetable()
	if !early.End.Equal(start.Add(48 * time.Hour)) {
		t.Errorf("an end before the midpoint was served: end = %s", early.End)
	}
}
