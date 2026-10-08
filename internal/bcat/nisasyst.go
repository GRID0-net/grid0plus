package bcat

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"strings"
	"sync"
)

// nisasyst is the scheme Splatoon 3 encrypts its BCAT packs with: AES-128-CBC,
// where the key is served by GetFestDecryptionKey and the IV is derived from
// the pack's resource path.
//
// Both inputs are the server's to choose, which is the whole reason a genuinely
// custom Splatfest is possible rather than a rename of one of Nintendo's. It is
// also why the resource name is case-sensitive in a way nothing about the name
// suggests: change one character and the IV changes, the pack does not open,
// and the console reports a generic communication error.

// ErrNoKeyMaterial is returned when the IV lookup table has not been supplied.
var ErrNoKeyMaterial = errors.New("bcat: no key material configured")

// keyMaterialLen is the length of the IV-derivation lookup table, in hex
// characters. It is a table of 256 hex digits, indexed a nibble at a time.
const keyMaterialLen = 256

var (
	keyMaterialOnce sync.Once
	keyMaterial     string
	keyMaterialErr  error
)

// KeyMaterialPath is where the IV-derivation table is read from.
//
// It is NOT in this repository, and must not be: the table is lifted from the
// game binary, and CLAUDE.md's first rule is that no Nintendo key, certificate,
// ticket or dumped file is committed here. The operator supplies it, the same
// way Nextendo's own server loads its measured responses from a directory
// rather than shipping them.
//
// Without it, everything in this package that needs an IV fails with a message
// saying exactly what is missing, rather than producing packs that silently do
// not open.
const KeyMaterialPath = "SWITCHNET_BCAT_KEY_MATERIAL"

// DefaultKeyMaterialFile is read when KeyMaterialPath is unset, so a server
// that holds the table there needs nothing set per command. The file lives on
// the server only (root:switchnet 0640), never in this repository.
var DefaultKeyMaterialFile = "/etc/switchnet/bcat-key-material"

// loadKeyMaterial reads the IV-derivation table once.
func loadKeyMaterial() (string, error) {
	keyMaterialOnce.Do(func() {
		path := os.Getenv(KeyMaterialPath)
		if path == "" {
			if _, err := os.Stat(DefaultKeyMaterialFile); err != nil {
				keyMaterialErr = fmt.Errorf("%w: set %s, or install %s, with the %d-character table",
					ErrNoKeyMaterial, KeyMaterialPath, DefaultKeyMaterialFile, keyMaterialLen)
				return
			}
			path = DefaultKeyMaterialFile
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			keyMaterialErr = fmt.Errorf("bcat: read key material: %w", err)
			return
		}
		s := strings.TrimSpace(string(raw))
		if len(s) != keyMaterialLen {
			keyMaterialErr = fmt.Errorf("bcat: key material is %d characters, want %d",
				len(s), keyMaterialLen)
			return
		}
		keyMaterial = s
	})
	return keyMaterial, keyMaterialErr
}

// deriveIV builds a pack's AES initialisation vector from its resource path.
//
// CRC-32 of the path seeds sead::Random; each of the sixteen IV bytes is then
// two hex digits, each taken from the lookup table at the index in the TOP byte
// of a fresh 32-bit draw. Two draws per byte, thirty-two in total.
func deriveIV(resourcePath string, table string) ([]byte, error) {
	if len(table) != keyMaterialLen {
		return nil, fmt.Errorf("bcat: key material is %d characters, want %d", len(table), keyMaterialLen)
	}

	rng := newSeadRandom(crc32.ChecksumIEEE([]byte(resourcePath)))

	iv := make([]byte, aes.BlockSize)
	for i := range iv {
		hi := table[rng.nextU32()>>24]
		lo := table[rng.nextU32()>>24]
		b, err := hexPair(hi, lo)
		if err != nil {
			return nil, err
		}
		iv[i] = b
	}
	return iv, nil
}

func hexPair(hi, lo byte) (byte, error) {
	h, err := hexDigit(hi)
	if err != nil {
		return 0, err
	}
	l, err := hexDigit(lo)
	if err != nil {
		return 0, err
	}
	return h<<4 | l, nil
}

func hexDigit(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, fmt.Errorf("bcat: key material contains %q, which is not a hex digit", c)
}

// ResourcePath returns the string a pack's IV is derived from.
//
// The base pack is named after the resource alone; every other pack has its
// short name appended. This asymmetry is not ours and is not guessable — it is
// what the game does, so a base pack built as "<resource>_base.pack.zs" derives
// a different IV and never opens.
func ResourcePath(resourceID, packName string) string {
	if packName == PackBase {
		return resourceID + ".pack.zs"
	}
	return resourceID + "_" + packName + ".pack.zs"
}

// The packs a fest is made of, in the order GetFestDecryptionKey returns their
// keys. wa/wb/wc are the three teams.
const (
	PackBase       = "base"
	PackBattleHelp = "bh"
	PackTeamAlpha  = "wa"
	PackTeamBravo  = "wb"
	PackTeamChar   = "wc"
)

// PackOrder is the packs a seed folder holds, in a stable order.
//
// Note what this is NOT: the order GetFestDecryptionKey returns its keys in.
// There are five packs and SIX key fields, so no positional mapping between the
// two can be right — KeyFields and PackForKeyField below are the mapping.
var PackOrder = []string{PackBase, PackBattleHelp, PackTeamAlpha, PackTeamBravo, PackTeamChar}

// The key fields FestDecryptionKey declares, in the order it declares them.
//
// GetFestDecryptionKey returns keys POSITIONALLY, so this order is the contract:
// a key in the wrong slot is handed to the console for the wrong phase, the pack
// fails to open, and nothing in the failure says which phase was wrong.
const (
	KeyNotice            = "notice"
	KeyStart             = "start"
	KeyResultWinningTeam = "result_winning_team"
	KeyTeamAlpha         = "team_alpha"
	KeyTeamBravo         = "team_bravo"
	KeyTeamCharlie       = "team_charlie"
)

// KeyFields is the key-field order, which is also the order keys are stored in.
var KeyFields = []string{
	KeyNotice, KeyStart, KeyResultWinningTeam,
	KeyTeamAlpha, KeyTeamBravo, KeyTeamCharlie,
}

// PackForKeyField maps each key field onto the pack its key opens.
//
// The optional team slots retain each outcome archive's key in storage. They
// are not all published on ordinary calls: toyohr selects wa/wb/wc from the
// calculated winner and supplies that key in result_winning_team after close.
// There is therefore no static pack for the result slot in this map.
//
// TODO(capture-needed): confirm the base/notice and bh/start mapping against
// a successful native BCAT download whose phase keys and packs are available.
// The working reference server uses base, bh, then the winning outcome pack.
// Change this map if that comparison establishes a different phase mapping.
var PackForKeyField = map[string]string{
	KeyNotice:            PackBase,
	KeyStart:             PackBattleHelp,
	KeyResultWinningTeam: "",
	KeyTeamAlpha:         PackTeamAlpha,
	KeyTeamBravo:         PackTeamBravo,
	KeyTeamCharlie:       PackTeamChar,
}

// KeysInFieldOrder renders per-pack keys as the positional list a fest row
// stores and GetFestDecryptionKey returns.
//
// A field with no pack, or a pack that was not built, gets an EMPTY slot rather
// than being left out. Nintendo omits the key for a phase that has not happened
// yet, and an empty string is a key the console would try to use — so the
// consumer skips empties, and the slot keeps every later key in position.
func KeysInFieldOrder(keys map[string][]byte) []string {
	out := make([]string, 0, len(KeyFields))
	for _, field := range KeyFields {
		pack := PackForKeyField[field]
		key, ok := keys[pack]
		if pack == "" || !ok {
			out = append(out, "")
			continue
		}
		out = append(out, hex.EncodeToString(key))
	}
	return out
}

// Decrypt turns an encrypted pack into its zstd-compressed contents.
//
// The file is a 4-byte little-endian length followed by AES-CBC ciphertext.
// The ciphertext is padded up to the block size and the length says how much of
// the plaintext is real -- there is no padding scheme to strip, so trusting the
// block size instead would hand zstd up to fifteen bytes of rubbish.
func Decrypt(resourceID, packName string, key, encrypted []byte) ([]byte, error) {
	table, err := loadKeyMaterial()
	if err != nil {
		return nil, err
	}
	return DecryptWith(resourceID, packName, key, encrypted, table)
}

// DecryptWith is Decrypt against a caller-supplied lookup table, so a test can
// exercise the whole path without the operator's file.
func DecryptWith(resourceID, packName string, key, encrypted []byte, table string) ([]byte, error) {
	if len(encrypted) < 4 {
		return nil, fmt.Errorf("bcat: pack is %d bytes, too short for its length prefix", len(encrypted))
	}
	size := binary.LittleEndian.Uint32(encrypted[:4])
	body := encrypted[4:]

	if len(body)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("bcat: ciphertext is %d bytes, not a multiple of the %d-byte block size",
			len(body), aes.BlockSize)
	}
	if int(size) > len(body) {
		return nil, fmt.Errorf("bcat: pack claims %d bytes of content but carries only %d", size, len(body))
	}

	block, err := newCipher(key)
	if err != nil {
		return nil, err
	}
	iv, err := deriveIV(ResourcePath(resourceID, packName), table)
	if err != nil {
		return nil, err
	}

	out := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, body)
	return out[:size], nil
}

// Encrypt is Decrypt's inverse: it compresses nothing and encrypts the
// zstd-compressed bytes it is given, prefixed with their real length.
func Encrypt(resourceID, packName string, key, compressed []byte) ([]byte, error) {
	table, err := loadKeyMaterial()
	if err != nil {
		return nil, err
	}
	return EncryptWith(resourceID, packName, key, compressed, table)
}

// EncryptWith is Encrypt against a caller-supplied lookup table.
func EncryptWith(resourceID, packName string, key, compressed []byte, table string) ([]byte, error) {
	block, err := newCipher(key)
	if err != nil {
		return nil, err
	}
	iv, err := deriveIV(ResourcePath(resourceID, packName), table)
	if err != nil {
		return nil, err
	}

	// Pad up to the block size. The padding bytes are never read back -- the
	// length prefix is what bounds the content -- so they are zeros rather than
	// a padding scheme the console would have to agree with.
	padded := make([]byte, roundUp(len(compressed), aes.BlockSize))
	copy(padded, compressed)

	out := make([]byte, 4+len(padded))
	binary.LittleEndian.PutUint32(out[:4], uint32(len(compressed)))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out[4:], padded)
	return out, nil
}

func newCipher(key []byte) (cipher.Block, error) {
	if len(key) != 16 {
		return nil, fmt.Errorf("bcat: key is %d bytes, want 16 (AES-128)", len(key))
	}
	return aes.NewCipher(key)
}

func roundUp(n, to int) int {
	if n%to == 0 {
		return n
	}
	return n + to - n%to
}

// DeriveIVForTest exposes deriveIV so a differential test can compare this
// implementation against the reference tooling on identical inputs.
//
// Exported rather than kept internal because that comparison is the only
// evidence that the derivation is right: it was reimplemented from a reference,
// and a subtly wrong IV produces packs that are silently unopenable rather than
// anything that fails visibly here.
func DeriveIVForTest(resourcePath, table string) ([]byte, error) {
	return deriveIV(resourcePath, table)
}
