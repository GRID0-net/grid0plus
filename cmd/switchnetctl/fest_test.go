package main

import (
	"testing"

	"github.com/n-popescu/switchnet/internal/bcat"
)

// TestGeneratePackKeysPositionsAreStable guards the one thing that would be
// silent if wrong: a key landing in the wrong positional slot of packKeys
// means SaveFest hands the console a key for the wrong phase, and the pack
// fails to decrypt with nothing pointing at which slot disagreed.
func TestGeneratePackKeysPositionsAreStable(t *testing.T) {
	packs := bcat.Packs{
		bcat.PackBase:      []byte("base"),
		bcat.PackTeamAlpha: []byte("wa"),
		bcat.PackTeamChar:  []byte("wc"),
		// bh and wb deliberately absent, like a fest missing those packs.
	}

	seedKeys, packKeys, err := generatePackKeys(packs)
	if err != nil {
		t.Fatal(err)
	}

	if len(packKeys) != len(bcat.PackOrder) {
		t.Fatalf("packKeys has %d entries, want %d (one per bcat.PackOrder slot)",
			len(packKeys), len(bcat.PackOrder))
	}
	for i, name := range bcat.PackOrder {
		_, built := packs[name]
		if built && packKeys[i] == "" {
			t.Errorf("pack %q (slot %d) was built but its packKeys slot is empty", name, i)
		}
		if !built && packKeys[i] != "" {
			t.Errorf("pack %q (slot %d) was not built but its packKeys slot is %q, want empty",
				name, i, packKeys[i])
		}
	}

	// Every key in packKeys must be a 32-hex-character (16-byte) AES-128
	// key, and it must be the exact same bytes seedKeys has for that pack --
	// two independently-generated keys for the same pack would mean the seed
	// folder and the registered fest disagree about how to decrypt it.
	for _, name := range bcat.PackOrder {
		if _, built := packs[name]; !built {
			continue
		}
		key, ok := seedKeys[name]
		if !ok {
			t.Errorf("pack %q has no entry in seedKeys", name)
			continue
		}
		if len(key) != 16 {
			t.Errorf("pack %q's seedKeys entry is %d bytes, want 16", name, len(key))
		}
	}

	// Two calls must not reuse a key -- a repeated key across fests would
	// mean compromising one pack's decryption compromises every other fest
	// built the same way.
	seedKeys2, _, err := generatePackKeys(packs)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range seedKeys {
		if string(key) == string(seedKeys2[name]) {
			t.Errorf("pack %q got the same key across two calls to generatePackKeys", name)
		}
	}
}

// TestGeneratePackKeysRejectsNoPacks matches bcat.BuildPacks's own refusal to
// produce an empty Packs map -- generatePackKeys should still behave
// sensibly (all-empty packKeys, no error) if it is ever called with one
// anyway, rather than panicking on an empty bcat.PackOrder walk.
func TestGeneratePackKeysHandlesNoPacks(t *testing.T) {
	seedKeys, packKeys, err := generatePackKeys(bcat.Packs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(seedKeys) != 0 {
		t.Errorf("seedKeys = %v, want empty", seedKeys)
	}
	for i, k := range packKeys {
		if k != "" {
			t.Errorf("packKeys[%d] = %q, want empty", i, k)
		}
	}
}

func TestPackNamesFollowsPackOrder(t *testing.T) {
	packs := bcat.Packs{
		bcat.PackTeamChar:  []byte("wc"),
		bcat.PackBase:      []byte("base"),
		bcat.PackTeamAlpha: []byte("wa"),
	}
	got := packNames(packs)
	want := []string{bcat.PackBase, bcat.PackTeamAlpha, bcat.PackTeamChar}
	if len(got) != len(want) {
		t.Fatalf("packNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("packNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
