package bcat

// sead::Random, Nintendo's standard-library PRNG.
//
// It is here because Splatoon 3 derives a BCAT pack's AES initialisation vector
// from it. Nothing else in GRID0+ needs a PRNG with these exact outputs, and
// nothing here should ever be used as a source of randomness for anything that
// needs to be unpredictable: it is a small xorshift seeded from a CRC-32, and
// reproducing Nintendo's exact sequence is its only purpose.

// seadRandom reproduces sead::Random.
type seadRandom struct {
	state [4]uint32
}

// newSeadRandom mirrors sead::Random::init(u32).
//
// The multiplier and the tempering shift are the ones sead uses; they are not a
// choice and changing them produces an IV the console will not agree with.
func newSeadRandom(seed uint32) *seadRandom {
	r := &seadRandom{}
	s := seed
	for i := 0; i < 4; i++ {
		s = 0x6C078965*(s^(s>>30)) + uint32(i) + 1
		r.state[i] = s
	}
	return r
}

// nextU32 mirrors sead::Random::getU32().
func (r *seadRandom) nextU32() uint32 {
	v1 := r.state[0] ^ (r.state[0] << 11)
	r.state[0] = r.state[1]
	v2 := r.state[3]
	v3 := v1 ^ (v1 >> 8) ^ v2 ^ (v2 >> 19)
	r.state[1] = r.state[2]
	r.state[2] = v2
	r.state[3] = v3
	return v3
}
