package bcat

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// testTable is a SYNTHETIC IV-derivation table.
//
// The real one is lifted from the game binary and is not in this repository —
// see KeyMaterialPath. It is not needed here: the table is an opaque lookup of
// hex digits, so a synthetic one exercises exactly the same code and every
// expected value below was produced by running the reference implementation
// against this same synthetic table.
//
// The implementation was ALSO verified against the reference on the real table,
// outside the test suite: all nine derivations matched byte for byte, including
// the empty path. That check cannot live here without committing the table,
// which is why these vectors are synthetic and that one is recorded in the
// commit message instead.
const testTable = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestSeadRandomMatchesTheReferenceSequence.
//
// sead::Random is Nintendo's standard PRNG, and the IV derivation is nothing
// but a walk over its output. If the sequence is wrong the IV is wrong, and a
// wrong IV produces a pack the console cannot open with no error that points
// anywhere near here.
func TestSeadRandomMatchesTheReferenceSequence(t *testing.T) {
	for _, tc := range []struct {
		seed uint32
		want []uint32
	}{
		{0x0, []uint32{0x4807714d, 0x181b859b, 0x3f2c551a, 0x4cda87eb}},
		{0x1, []uint32{0x53c36017, 0x1c7be1b6, 0x34dd5f8b, 0xcb5026ae}},
		{0xdeadbeef, []uint32{0x2812bed2, 0x4414675b, 0xbbb12012, 0x8980cb64}},
	} {
		r := newSeadRandom(tc.seed)
		for i, want := range tc.want {
			if got := r.nextU32(); got != want {
				t.Errorf("seed %#x draw %d = %#010x, want %#010x", tc.seed, i, got, want)
			}
		}
	}
}

// TestTheIVIsDerivedFromTheResourcePath pins the derivation itself.
func TestTheIVIsDerivedFromTheResourcePath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{"juea-lovefes2.pack.zs", "1c8d43fff4db36591025ba244b3796f9"},
		{"juea-lovefes2_bh.pack.zs", "6f2c736a9f9074e0a3d08969071644ed"},
		{"nxtd-00042.pack.zs", "9d991c26bc57b49932a70601adfb2fe9"},
		{"", "88fcfb6e1140d990690fdb4dca9304ee"},
	} {
		iv, err := deriveIV(tc.path, testTable)
		if err != nil {
			t.Fatalf("%q: %v", tc.path, err)
		}
		if got := hex.EncodeToString(iv); got != tc.want {
			t.Errorf("%q derived %s, want %s", tc.path, got, tc.want)
		}
	}
}

// TestOneWrongCharacterInTheResourceNameChangesTheIV.
//
// This is the reason the resource name is forced lowercase in internal/toyohr,
// and the reason that rule is worth a validation error rather than a silent
// correction: the failure it prevents is a pack that does not open, reported to
// the player as a generic communication error with nothing naming the cause.
func TestOneWrongCharacterInTheResourceNameChangesTheIV(t *testing.T) {
	right, err := deriveIV("juea-lovefes2.pack.zs", testTable)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := deriveIV("juea-Lovefes2.pack.zs", testTable)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(right, wrong) {
		t.Error("a capitalised resource name derived the same IV; the case rule would be pointless")
	}
}

// TestTheBasePackIsNamedDifferentlyFromEveryOtherPack.
//
// The base pack's IV path is the resource alone; every other pack has its short
// name appended. Nothing about the layout suggests that asymmetry, and getting
// it wrong breaks exactly one of the five packs — the largest one.
func TestTheBasePackIsNamedDifferentlyFromEveryOtherPack(t *testing.T) {
	if got, want := ResourcePath("juea-lovefes2", PackBase), "juea-lovefes2.pack.zs"; got != want {
		t.Errorf("base path = %q, want %q", got, want)
	}
	for _, pack := range []string{PackBattleHelp, PackTeamAlpha, PackTeamBravo, PackTeamChar} {
		want := "juea-lovefes2_" + pack + ".pack.zs"
		if got := ResourcePath("juea-lovefes2", pack); got != want {
			t.Errorf("%s path = %q, want %q", pack, got, want)
		}
	}
}

// TestAPackRoundTripsThroughEncryptAndDecrypt, including the awkward sizes:
// the container pads ciphertext to the block size but records the real length,
// so a payload that is not a multiple of 16 must come back at its own length
// and not one rounded up.
func TestAPackRoundTripsThroughEncryptAndDecrypt(t *testing.T) {
	key := mustHex(t, "000102030405060708090a0b0c0d0e0f")

	for _, size := range []int{0, 1, 15, 16, 17, 6455, 4096} {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(i * 7)
		}

		enc, err := EncryptWith("juea-lovefes2", PackBattleHelp, key, payload, testTable)
		if err != nil {
			t.Fatalf("size %d: encrypt: %v", size, err)
		}
		if (len(enc)-4)%16 != 0 {
			t.Errorf("size %d: ciphertext is %d bytes, not block-aligned", size, len(enc)-4)
		}

		got, err := DecryptWith("juea-lovefes2", PackBattleHelp, key, enc, testTable)
		if err != nil {
			t.Fatalf("size %d: decrypt: %v", size, err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("size %d: round trip returned %d bytes, want %d", size, len(got), size)
		}
	}
}

// TestDecryptingWithTheWrongPackNameFails is the mismatch that actually happens:
// the packs sit in one directory and it is easy to hand the wrong name along
// with the right bytes.
func TestDecryptingWithTheWrongPackNameFails(t *testing.T) {
	key := mustHex(t, "000102030405060708090a0b0c0d0e0f")
	payload := []byte("the quick brown fox jumps over the lazy dog")

	enc, err := EncryptWith("juea-lovefes2", PackTeamAlpha, key, payload, testTable)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecryptWith("juea-lovefes2", PackTeamBravo, key, enc, testTable)
	if err != nil {
		// An error is a fine outcome too.
		return
	}
	if bytes.Equal(got, payload) {
		t.Error("a pack decrypted correctly under the wrong pack name; the IV cannot be path-derived")
	}
}

func TestAMalformedPackIsRefusedRatherThanMisread(t *testing.T) {
	key := mustHex(t, "000102030405060708090a0b0c0d0e0f")

	for name, input := range map[string][]byte{
		"shorter than its length prefix": {0x01, 0x02},
		"ciphertext not block-aligned":   {0x04, 0x00, 0x00, 0x00, 0xaa, 0xbb},
		"claims more content than it carries": append(
			[]byte{0xff, 0xff, 0x00, 0x00}, make([]byte, 16)...),
	} {
		if _, err := DecryptWith("r", PackBase, key, input, testTable); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAKeyThatIsNotAES128IsRefused(t *testing.T) {
	for _, n := range []int{0, 8, 24, 32} {
		_, err := EncryptWith("r", PackBase, make([]byte, n), []byte("x"), testTable)
		if err == nil {
			t.Errorf("a %d-byte key was accepted; nisasyst is AES-128", n)
		}
	}
}

func TestMissingKeyMaterialSaysWhatIsMissing(t *testing.T) {
	_, err := deriveIV("x", "too short")
	if err == nil {
		t.Fatal("a short table was accepted")
	}
	if !strings.Contains(err.Error(), "key material") {
		t.Errorf("error does not name the missing thing: %v", err)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestEveryPackHasExactlyOneKeyField guards the mapping between the five packs a
// seed folder holds and the six key fields GetFestDecryptionKey declares.
//
// There is no positional mapping between five and six, and pretending there was
// one is the bug this test exists to keep out: with PackOrder used as a key
// order, wa's key landed in result_winning_team, wb's in team_alpha, and every
// team pack failed to open — with nothing in the failure naming a phase.
func TestEveryPackHasExactlyOneKeyField(t *testing.T) {
	if len(KeyFields) != 6 {
		t.Fatalf("KeyFields has %d entries, want the 6 the message declares", len(KeyFields))
	}

	seen := map[string]string{}
	for _, field := range KeyFields {
		pack, ok := PackForKeyField[field]
		if !ok {
			t.Errorf("key field %q has no entry in PackForKeyField", field)
			continue
		}
		if pack == "" {
			continue
		}
		if other, dup := seen[pack]; dup {
			t.Errorf("pack %q is claimed by both %q and %q", pack, other, field)
		}
		seen[pack] = field
	}

	for _, pack := range PackOrder {
		if _, ok := seen[pack]; !ok {
			t.Errorf("pack %q has no key field, so its key can never reach the console", pack)
		}
	}

	// The certain half: nothing else reads sensibly.
	for field, wantPack := range map[string]string{
		KeyTeamAlpha:   PackTeamAlpha,
		KeyTeamBravo:   PackTeamBravo,
		KeyTeamCharlie: PackTeamChar,
	} {
		if got := PackForKeyField[field]; got != wantPack {
			t.Errorf("%s opens %q, want %q", field, got, wantPack)
		}
	}
}

func TestKeysInFieldOrderKeepsAbsentPacksAsEmptySlots(t *testing.T) {
	// A two-team fest has no wc, and result_winning_team has no pack at all.
	// Both must be empty slots rather than omissions, or every later key shifts
	// into the wrong phase.
	keys := KeysInFieldOrder(map[string][]byte{
		PackBase:      make([]byte, 16),
		PackTeamAlpha: {1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
	})

	if len(keys) != len(KeyFields) {
		t.Fatalf("got %d slots, want one per key field (%d)", len(keys), len(KeyFields))
	}
	for i, field := range KeyFields {
		switch field {
		case KeyNotice:
			if len(keys[i]) != 32 {
				t.Errorf("%s is %q, want base's key", field, keys[i])
			}
		case KeyTeamAlpha:
			if keys[i] != "0102030405060708090a0b0c0d0e0f10" {
				t.Errorf("%s is %q, want wa's key", field, keys[i])
			}
		default:
			if keys[i] != "" {
				t.Errorf("%s is %q, want an empty slot", field, keys[i])
			}
		}
	}
}
