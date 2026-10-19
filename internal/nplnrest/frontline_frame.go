package nplnrest

import (
	"encoding/binary"
	"time"
)

// frontlineOpeningFrame builds the size-prefixed FlatBuffer a console expects
// as the first message on a frontline connection: discriminator 12, an empty
// payload, sender "fro", and Unix seconds.
//
// UNCONFIRMED: the semantic name of type 12 and any later messages or
// acknowledgements. This encoder covers only the connection opening.
func frontlineOpeningFrame(now time.Time) []byte {
	b := make([]byte, 68)
	put16 := func(off int, v uint16) { binary.LittleEndian.PutUint16(b[off:], v) }
	put32 := func(off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }
	put32(0, 64)  // Size prefix excludes itself.
	put32(4, 20)  // Root table at absolute offset 24.
	put16(12, 12) // Root vtable size: header plus four field offsets.
	put16(14, 28) // Root object size.
	for i, off := range []uint16{23, 24, 16, 4} {
		put16(16+i*2, off)
	}
	put32(24, 12) // Distance back to the root vtable.
	binary.LittleEndian.PutUint64(b[28:], uint64(now.Unix()))
	put32(40, 12) // Sender string at absolute offset 52.
	b[47] = 12    // Captured payload discriminator; its name is not established.
	put32(48, 16) // Empty payload table at absolute offset 64.
	put32(52, 3)
	copy(b[56:], "fro")
	put16(60, 4) // Empty payload vtable.
	put16(62, 4) // Empty payload object size.
	put32(64, 4) // Distance back to its vtable.
	return b
}
