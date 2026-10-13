package splat3save

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"math/bits"
	"os"
)

// Table is the 32-word table in Splatoon 3's main (0x71050b6dec in 11.3.0)
// the save key is chosen from. It is the game's key material, so it is never
// in this repository: the operator supplies it as a 128-byte little-endian
// file, the way internal/bcat takes its IV table.
type Table [32]uint32

// LoadTable reads an operator's table file.
func LoadTable(path string) (*Table, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) != 128 {
		return nil, fmt.Errorf("splat3save: table %s is %d bytes, want 128", path, len(b))
	}
	var t Table
	for i := range t {
		t[i] = binary.LittleEndian.Uint32(b[4*i:])
	}
	return &t, nil
}

// keyIV is FUN_71011955dc: the key and IV are the top byte of 48 draws of
// sead::Random seeded with the table word the file's seed selects.
func (t *Table) keyIV(seed uint32) (v uint32, key, iv []byte) {
	v = t[newSeadRandom(seed).nextU32()>>27]
	r := newSeadRandom(v)
	b := make([]byte, 48)
	for i := range b {
		b[i] = byte(r.nextU32() >> 24)
	}
	return v, b[:32], b[32:]
}

// decrypt opens the container: [u32 seed][u32 check][AES-256-CTR body].
// The check is verified first, as the game does, so a file from another
// table (another game version) is a clear refusal rather than garbage.
func decrypt(file []byte, t *Table) ([]byte, error) {
	if len(file) < 8 {
		return nil, fmt.Errorf("splat3save: %d bytes is too short to be a save", len(file))
	}
	seed := binary.LittleEndian.Uint32(file)
	check := binary.LittleEndian.Uint32(file[4:])
	body := file[8:]
	v, key, iv := t.keyIV(seed)
	if murmur3(body, seed)+newSeadRandom(v).nextU32() != check {
		return nil, ErrCheck
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(body))
	cipher.NewCTR(block, iv).XORKeyStream(out, body)
	return out, nil
}

// Encrypt is the inverse of Decode's decryption: it wraps a plaintext in the
// container the game reads. Tests build saves with it.
func Encrypt(plain []byte, seed uint32, t *Table) []byte {
	v, key, iv := t.keyIV(seed)
	block, _ := aes.NewCipher(key)
	out := make([]byte, 8+len(plain))
	cipher.NewCTR(block, iv).XORKeyStream(out[8:], plain)
	binary.LittleEndian.PutUint32(out, seed)
	binary.LittleEndian.PutUint32(out[4:], murmur3(out[8:], seed)+newSeadRandom(v).nextU32())
	return out
}

// seadRandom is sead::Random. internal/bcat has its own copy; the two are
// kept apart so neither package exports a PRNG nobody should use for
// anything but matching Nintendo's sequence.
type seadRandom struct{ s [4]uint32 }

func newSeadRandom(seed uint32) *seadRandom {
	r := &seadRandom{}
	s := seed
	for i := range r.s {
		s = 0x6C078965*(s^(s>>30)) + uint32(i) + 1
		r.s[i] = s
	}
	return r
}

func (r *seadRandom) nextU32() uint32 {
	t := r.s[0] ^ (r.s[0] << 11)
	x3 := r.s[3]
	n := x3 ^ (x3 >> 19) ^ t ^ (t >> 8)
	r.s = [4]uint32{r.s[1], r.s[2], x3, n}
	return n
}

// murmur3 is MurmurHash3_x86_32. The game uses it twice: for the file's
// check and, seeded with 0, to hash the save's key names.
func murmur3(data []byte, seed uint32) uint32 {
	const c1, c2 = 0xcc9e2d51, 0x1b873593
	h := seed
	n := len(data) / 4
	for i := 0; i < n; i++ {
		k := binary.LittleEndian.Uint32(data[4*i:])
		k = bits.RotateLeft32(k*c1, 15) * c2
		h ^= k
		h = bits.RotateLeft32(h, 13)*5 + 0xe6546b64
	}
	var k uint32
	tail := data[4*n:]
	for i := len(tail) - 1; i >= 0; i-- {
		k = k<<8 | uint32(tail[i])
	}
	if len(tail) > 0 {
		h ^= bits.RotateLeft32(k*c1, 15) * c2
	}
	h ^= uint32(len(data))
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}
