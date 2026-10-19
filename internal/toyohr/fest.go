package toyohr

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// A Splatfest, as GRID0+ stores it.
//
// This is the *mechanics* of a fest and nothing else: when it runs, which
// regions see it, and which BCAT pack carries its content. The team names, the
// banners and the idol dialogue are not here and cannot be — they live inside
// the BCAT pack the console downloads separately, which is why serving a custom
// fest is a two-part problem. See internal/bcat.
type Fest struct {
	// ID is the fest identifier, e.g. "NXTD-00042".
	//
	// UPPERCASE, always. The console matches it against the id inside its own
	// BCAT cache metadata, which is uppercase; a lowercase id is simply a
	// different fest as far as the game is concerned, and it answers
	// BcatInvalid.
	ID string

	// Resource is the BCAT pack identifier.
	//
	// lowercase, always, and for a sharper reason than ID's: the console
	// derives the pack's AES decryption IV from this string. One wrong capital
	// changes the IV, the pack does not decrypt, and the error surfaces as a
	// generic communication failure in the lobby with nothing pointing at the
	// name.
	Resource string

	// Revision is the pack's 40-hex-character revision (a SHA-1 digest).
	// It must equal the revision the pack's own metadata carries.
	Revision string

	// Start is when the fest opens for play. Every other phase is derived from
	// it, so this is the only time the operator picks.
	Start time.Time

	// End, when set, replaces the standard end of play (48 h after Start).
	// Results still come out offsetClose-offsetEnd after it. The game only
	// checks that the phases are in order, so a longer second half is a
	// server-side choice; see Timetable.
	End time.Time

	// Regions the fest is published in.
	Regions []string
}

// Phase offsets from Start, in seconds.
//
// These are not chosen: they are the intervals Splatoon 3 expects between a
// fest's phases, and the game reads a fest whose phases are spaced differently
// as being in a state it did not expect.
//
// Every one of them is a multiple of slotSeconds, which is what makes aligning
// Start enough to align the whole table.
const (
	offsetOpen  = -662400 // announced 7d16h before it starts
	offsetStart = 0
	offsetMid   = 86400  // 24h
	offsetEnd   = 172800 // 48h
	offsetClose = 180000 // 50h — results, 2h after play ends
)

// Publication window, relative to the announcement.
const (
	publishBeforeOpen = 25200   // opens 7h before the announcement
	publishDuration   = 3628800 // and runs 42 days, well past the fest itself
)

// slotSeconds is the granularity Splatoon 3 buckets its schedules into.
//
// TWO HOURS, and this is the single most expensive detail in the whole fest
// path. The game files every scheduled thing into a two-hour slot. A fest whose
// timestamps are not on a slot boundary belongs to no slot: the game stops
// treating it as an open fest and starts demanding its VERDICT instead, which a
// real console never does mid-fest. The lobby then fails for every player at
// once, with nothing in any log to say why.
//
// Since all the phase offsets above are multiples of this, aligning Start
// aligns open, mid, end and close for free.
const slotSeconds = 7200

// DefaultRegions are the four regions a fest is published in.
var DefaultRegions = []string{"JP", "US", "EU", "AP"}

// AlignToSlot rounds t down to the two-hour boundary Splatoon 3 requires.
//
// Down rather than to-nearest: rounding up would move a fest the operator
// scheduled later than they asked for, and a fest that starts before its
// announced time is the harmless direction.
func AlignToSlot(t time.Time) time.Time {
	return t.UTC().Truncate(slotSeconds * time.Second)
}

// IsSlotAligned reports whether t sits on a slot boundary.
func IsSlotAligned(t time.Time) bool {
	return t.UTC().Unix()%slotSeconds == 0
}

// NextFestStart returns the next Saturday at midnight UTC after now.
//
// Saturday because that is when fests conventionally begin, and because a
// default that lands on a predictable day is easier for an operator to reason
// about than "72 hours from whenever you clicked". Already slot-aligned:
// midnight is a multiple of two hours.
func NextFestStart(now time.Time) time.Time {
	day := now.UTC().Truncate(24 * time.Hour)
	for day.Weekday() != time.Saturday || !day.After(now.UTC()) {
		day = day.Add(24 * time.Hour)
	}
	return AlignToSlot(day)
}

// Timetable is a fest's five phase times.
type Timetable struct {
	Open  time.Time
	Start time.Time
	Mid   time.Time
	End   time.Time
	Close time.Time
}

// Timetable derives the five phase times from the fest's start.
//
// The start is aligned first, which aligns the whole table, because every
// offset is a multiple of the slot length.
func (f Fest) Timetable() Timetable {
	start := AlignToSlot(f.Start)
	at := func(offset int) time.Time {
		return start.Add(time.Duration(offset) * time.Second)
	}
	tt := Timetable{
		Open:  at(offsetOpen),
		Start: at(offsetStart),
		Mid:   at(offsetMid),
		End:   at(offsetEnd),
		Close: at(offsetClose),
	}
	// An operator's end of play, aligned like Start. Ignored unless it falls
	// after the midpoint: the game's fest state machine treats a timetable out of
	// order as no fest at all.
	if end := AlignToSlot(f.End); !f.End.IsZero() && end.After(tt.Mid) {
		tt.End = end
		tt.Close = end.Add((offsetClose - offsetEnd) * time.Second)
	}
	return tt
}

// PublishWindow returns the window a region's fest data is published in.
func (f Fest) PublishWindow() (start, end time.Time) {
	start = f.Timetable().Open.Add(-publishBeforeOpen * time.Second)
	return start, start.Add(publishDuration * time.Second)
}

// Phase is where a fest is in its life.
type Phase int

const (
	// PhaseNone means the fest has not been announced yet.
	PhaseNone Phase = iota
	// PhaseAnnounced means it is visible but not yet playable.
	PhaseAnnounced
	// PhaseFirstHalf means play is open, before the midpoint.
	PhaseFirstHalf
	// PhaseSecondHalf means play is open, after the midpoint.
	PhaseSecondHalf
	// PhaseCounting means play has ended and results are not out.
	PhaseCounting
	// PhaseClosed means the results are published.
	PhaseClosed
)

func (p Phase) String() string {
	switch p {
	case PhaseAnnounced:
		return "announced"
	case PhaseFirstHalf:
		return "first_half"
	case PhaseSecondHalf:
		return "second_half"
	case PhaseCounting:
		return "counting"
	case PhaseClosed:
		return "closed"
	default:
		return "none"
	}
}

// PhaseAt reports where the fest is at a given moment.
func (f Fest) PhaseAt(now time.Time) Phase {
	t := f.Timetable()
	switch {
	case now.Before(t.Open):
		return PhaseNone
	case now.Before(t.Start):
		return PhaseAnnounced
	case now.Before(t.Mid):
		return PhaseFirstHalf
	case now.Before(t.End):
		return PhaseSecondHalf
	case now.Before(t.Close):
		return PhaseCounting
	default:
		return PhaseClosed
	}
}

// festIDPattern is what a fest identifier has to look like.
//
// Four uppercase letters, a hyphen, five digits — the shape Nintendo's own ids
// use ("JUEA-00201"). GRID0+'s use the prefix NXTD so that a fest of ours is
// never mistaken for one of theirs in a save file or a log.
var festIDPattern = regexp.MustCompile(`^[A-Z]{4}-[0-9]{5}$`)

// NewFestID builds a stable identifier for a fest starting at the given time.
func NewFestID(start time.Time) string {
	return fmt.Sprintf("NXTD-%05d", AlignToSlot(start).Unix()/86400%100000)
}

// NewResource builds a DEFAULT BCAT pack identifier for a fest id.
//
// A default, and only that. A real fest's resource is a hand-picked slug --
// "juea-lovefes2" against fest JUEA-00210 -- not anything derived from the id,
// which was confirmed by reading a real seed folder's summary.yml. So this is
// what to call a pack nobody has named yet, and the operator overrides it
// whenever a pack already exists: the announced resource MUST equal the one in
// that pack's summary.yml, or the console looks for content it cannot find.
//
// Lowercased unconditionally, because the decryption IV is derived from this
// string and a stray capital produces a pack that never opens.
func NewResource(festID string) string {
	return strings.ToLower(festID) + "-fes"
}

// NewRevision derives a pack revision from the fest id and resource.
//
// Nintendo puts a 40-hex-character digest here. When GRID0+ serves a real
// pack, the revision must be the one that pack's own metadata carries, so this
// is only the default for a fest whose pack has not been built yet: stable for
// a given fest, distinct between fests, and — by construction — never equal to
// the revision of a pack that already exists.
func NewRevision(festID, resource string) string {
	sum := sha1.Sum([]byte("switchnet/fest/" + festID + "/" + resource))
	return hex.EncodeToString(sum[:])
}

// Validate reports why a fest cannot be served, or nil.
//
// Every check here is one that produces BcatInvalid or a silent lobby failure
// rather than a useful error on the console, which is why they are refused at
// the point a fest is created rather than at the point it is announced.
func (f Fest) Validate() error {
	if !festIDPattern.MatchString(f.ID) {
		return fmt.Errorf("fest id %q must look like NXTD-00042: four uppercase letters, a hyphen, five digits", f.ID)
	}
	if f.Resource == "" {
		return fmt.Errorf("fest %s has no BCAT resource name", f.ID)
	}
	if f.Resource != strings.ToLower(f.Resource) {
		// The console derives the decryption IV from this string. A capital
		// here means the pack never opens, and nothing in the failure points
		// back at the name.
		return fmt.Errorf("fest %s resource %q must be lowercase: the pack's decryption IV is derived from it", f.ID, f.Resource)
	}
	if len(f.Revision) != 40 {
		return fmt.Errorf("fest %s revision %q must be 40 hex characters, got %d", f.ID, f.Revision, len(f.Revision))
	}
	if _, err := hex.DecodeString(f.Revision); err != nil {
		return fmt.Errorf("fest %s revision %q is not hexadecimal", f.ID, f.Revision)
	}
	if f.Start.IsZero() {
		return fmt.Errorf("fest %s has no start time", f.ID)
	}
	if !IsSlotAligned(f.Start) {
		// Caught here rather than silently aligned: an operator who asked for
		// 13:00 should be told it becomes 12:00, not have it happen quietly.
		return fmt.Errorf("fest %s starts at %s, which is not on a two-hour slot boundary; "+
			"Splatoon 3 files schedules into two-hour slots and a fest in no slot makes the game demand its verdict instead of opening",
			f.ID, f.Start.UTC().Format(time.RFC3339))
	}
	if len(f.Regions) == 0 {
		return fmt.Errorf("fest %s is published in no regions, so no console will see it", f.ID)
	}
	return nil
}

// NewFest builds a valid fest starting at the given time.
//
// The start is aligned rather than rejected, because this is the constructor:
// a caller asking for a fest "next Saturday" wants one, not an error about
// slot boundaries. Validate is the strict path, for a fest being loaded.
func NewFest(start time.Time, regions []string) Fest {
	start = AlignToSlot(start)
	id := NewFestID(start)
	resource := NewResource(id)
	if len(regions) == 0 {
		regions = DefaultRegions
	}
	return Fest{
		ID:       id,
		Resource: resource,
		Revision: NewRevision(id, resource),
		Start:    start,
		Regions:  append([]string(nil), regions...),
	}
}
