// Package bcat builds the BCAT packs a custom Splatfest's content lives in.
// The installer path writes regional cache directories directly. Native
// list/data serving is opt-in through internal/stubs and requires a matching
// signature-check patch on the console; see the package README for the
// remaining manifest questions.
//
// What the console ends up with, per region:
//
//	bcat-seed/<region>-default/
//	    summary.yml          FestID, FestResourceID, FestResourceRevision
//	    base.pack.zs.enc     the fest's main content
//	    bh.pack.zs.enc       battle help
//	    wa/wb/wc.pack.zs.enc  one per team
//
// Layout and field names confirmed against a real seed folder produced by a
// working server (JUEA-00210, 2026-08-24).
package bcat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Seed is one fest's delivery cache, ready to install.
type Seed struct {
	// FestID is the uppercase fest identifier, e.g. "NXTD-00042".
	FestID string
	// ResourceID is the lowercase pack identifier, e.g. "juea-lovefes2".
	//
	// This is a name the operator chooses, NOT one derived from the fest id.
	// Real fests use a human-readable slug, and the announced schedule has to
	// carry this exact string: the console derives every pack's decryption IV
	// from it.
	ResourceID string
	// Revision is the 40-hex-character revision the schedule must announce.
	Revision string
	// Regions the seed is built for, lowercase.
	Regions []string
}

// Summary renders the summary.yml the console reads to learn which fest its
// cache holds.
//
// Written by hand rather than through a YAML library: the file is three fixed
// keys, and a marshaller would be free to reorder them, quote them, or fold
// long lines — none of which a parser on the console has to tolerate.
func (s Seed) Summary() string {
	return fmt.Sprintf("FestID: %s\nFestResourceID: %s\nFestResourceRevision: %s\n",
		s.FestID, s.ResourceID, s.Revision)
}

// Validate reports why a seed cannot be built.
func (s Seed) Validate() error {
	if s.FestID == "" || s.FestID != strings.ToUpper(s.FestID) {
		return fmt.Errorf("bcat: fest id %q must be uppercase", s.FestID)
	}
	if s.ResourceID == "" || s.ResourceID != strings.ToLower(s.ResourceID) {
		// The IV is derived from this string, so a capital here produces packs
		// the console cannot open and an error that names nothing.
		return fmt.Errorf("bcat: resource id %q must be lowercase: every pack's decryption IV is derived from it", s.ResourceID)
	}
	if len(s.Revision) != 40 {
		return fmt.Errorf("bcat: revision %q must be 40 hex characters", s.Revision)
	}
	if len(s.Regions) == 0 {
		return fmt.Errorf("bcat: no regions, so no console would load the seed")
	}
	return nil
}

// Packs maps a pack's short name to its UNencrypted contents.
//
// Packs holds the uncompressed SARC archives. WriteWith wraps each archive in
// zstd before encrypting it: the working LoveFest pack decrypts to a zstd
// frame, then decompresses to the same SARC bytes our builder produces.
type Packs map[string][]byte

// Write builds the seed folder under dir.
//
// keys are the AES-128 keys for each pack, by short name. A pack with no key is
// an error rather than a skip: a fest missing one of its packs fails on the
// console at the phase that needs it, which is hours after anyone would connect
// the two events.
func (s Seed) Write(dir string, packs Packs, keys map[string][]byte) error {
	table, err := loadKeyMaterial()
	if err != nil {
		return err
	}
	return s.WriteWith(dir, packs, keys, table)
}

// WriteWith is Write against a caller-supplied IV table, so a test can exercise
// the whole build-and-encrypt path without the operator's file.
//
// Same seam as EncryptWith and DecryptWith, for the same reason: the table is
// lifted from the game binary and is not in this repository, and a path that can
// only be exercised with it is a path nothing exercises.
func (s Seed) WriteWith(dir string, packs Packs, keys map[string][]byte, table string) error {
	if err := s.Validate(); err != nil {
		return err
	}

	for name := range packs {
		if _, ok := keys[name]; !ok {
			return fmt.Errorf("bcat: pack %q has no key", name)
		}
	}
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		return fmt.Errorf("bcat: create zstd encoder: %w", err)
	}
	defer encoder.Close()

	summary := s.Summary()
	for _, region := range s.Regions {
		regionDir := filepath.Join(dir, "bcat-seed", strings.ToLower(region)+"-default")
		if err := os.MkdirAll(regionDir, 0o755); err != nil {
			return fmt.Errorf("bcat: create %s: %w", regionDir, err)
		}

		if err := os.WriteFile(filepath.Join(regionDir, "summary.yml"), []byte(summary), 0o644); err != nil {
			return fmt.Errorf("bcat: write summary: %w", err)
		}

		// Every region gets the same summary — confirmed against a real seed,
		// where all four region directories carried an identical file. Only the
		// pack contents may differ, and in practice they often do not either.
		for _, name := range PackOrder {
			payload, ok := packs[name]
			if !ok {
				continue
			}
			compressed := encoder.EncodeAll(payload, nil)
			enc, err := EncryptWith(s.ResourceID, name, keys[name], compressed, table)
			if err != nil {
				return fmt.Errorf("bcat: encrypt %s: %w", name, err)
			}
			path := filepath.Join(regionDir, name+".pack.zs.enc")
			if err := os.WriteFile(path, enc, 0o644); err != nil {
				return fmt.Errorf("bcat: write %s: %w", path, err)
			}
		}
	}
	return nil
}

// ReadSummary parses a summary.yml from an existing seed.
//
// Used to read the id, resource and revision back out of a pack somebody else
// built, so the announced schedule can be aligned to it rather than guessed at.
// That alignment is the whole ballgame: announce a fest whose packs the cache
// does not hold and the game answers BcatInvalid.
func ReadSummary(raw []byte) (Seed, error) {
	var s Seed
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "FestID":
			s.FestID = value
		case "FestResourceID":
			s.ResourceID = value
		case "FestResourceRevision":
			s.Revision = value
		}
	}
	if s.FestID == "" || s.ResourceID == "" || s.Revision == "" {
		return Seed{}, fmt.Errorf("bcat: summary is missing one of FestID, FestResourceID, FestResourceRevision")
	}
	return s, nil
}
