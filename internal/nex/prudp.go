package nex

import (
	"crypto/hmac"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
)

// PRUDP "Lite": the packet format a Switch sends inside each WebSocket
// message. Reference: https://github.com/kinnay/NintendoClients/wiki/PRUDP-Protocol#lite-format
//
//	0x0  u8   magic 0x80
//	0x1  u8   length of the options
//	0x2  u16  payload length
//	0x4  u8   source stream type << 4 | destination stream type
//	0x5  u8   source port
//	0x6  u8   destination port
//	0x7  u8   fragment id
//	0x8  u16  flags << 4 | type
//	0xA  u16  sequence id
//	0xC       options, then payload
//
// Unlike V0/V1 there is no session id, no checksum and no payload
// encryption: the TLS tunnel the WebSocket runs in does that job. The one
// signature Lite keeps is on the client's CONNECT, and it proves only that the
// client knows the title's access key -- which is public -- so it is checked
// to reject stray traffic, not to establish anyone's identity.

// PacketType is a PRUDP packet's type.
type PacketType uint8

const (
	PacketSYN        PacketType = 0
	PacketCONNECT    PacketType = 1
	PacketDATA       PacketType = 2
	PacketDISCONNECT PacketType = 3
	PacketPING       PacketType = 4
)

func (t PacketType) String() string {
	switch t {
	case PacketSYN:
		return "SYN"
	case PacketCONNECT:
		return "CONNECT"
	case PacketDATA:
		return "DATA"
	case PacketDISCONNECT:
		return "DISCONNECT"
	case PacketPING:
		return "PING"
	}
	return fmt.Sprintf("type(%d)", uint8(t))
}

// PRUDP packet flags.
const (
	FlagACK      uint16 = 0x001
	FlagReliable uint16 = 0x002
	FlagNeedACK  uint16 = 0x004
	FlagHasSize  uint16 = 0x008
	FlagMultiACK uint16 = 0x200
)

// StreamRVSecure is the stream type Nintendo's games always use.
const StreamRVSecure = 10

const (
	liteMagic      = 0x80
	liteHeaderSize = 12

	optionSupport       = 0
	optionConnSig       = 1
	optionFragmentID    = 2
	optionUnreliableSeq = 3
	optionMaxSubstream  = 4
	optionLiteSignature = 0x80
)

// Packet is one decoded PRUDP-Lite packet.
type Packet struct {
	SourceType uint8
	DestType   uint8
	SourcePort uint8
	DestPort   uint8
	FragmentID uint8
	Type       PacketType
	Flags      uint16
	Sequence   uint16

	// MinorVersion and SupportedFunctions travel together in option 0, on
	// SYN and CONNECT in both directions.
	MinorVersion       uint8
	SupportedFunctions uint32

	// ConnectionSignature is option 1: the server's, in its SYN/ACK.
	ConnectionSignature []byte

	// LiteSignature is option 0x80: the client's proof, in its CONNECT.
	LiteSignature []byte

	Payload []byte
}

// Has reports whether every bit of f is set.
func (p *Packet) Has(f uint16) bool { return p.Flags&f == f }

// Encode serialises p.
//
// The options written are exactly the set Kinnay's client insists on, which
// it checks as a set and refuses the packet otherwise: {support, connection
// signature} on a SYN/ACK, {support, lite signature} on a CONNECT that is not
// an acknowledgement, {support} on any other SYN or CONNECT, and nothing on
// anything else.
func (p *Packet) Encode() []byte {
	var opts []byte
	put := func(id uint8, v []byte) {
		opts = append(opts, id, uint8(len(v)))
		opts = append(opts, v...)
	}
	if p.Type == PacketSYN || p.Type == PacketCONNECT {
		put(optionSupport, binary.LittleEndian.AppendUint32(nil,
			uint32(p.MinorVersion)|p.SupportedFunctions<<8))
		if p.Type == PacketSYN && p.Has(FlagACK) {
			put(optionConnSig, pad16(p.ConnectionSignature))
		}
		if p.Type == PacketCONNECT && !p.Has(FlagACK) {
			put(optionLiteSignature, pad16(p.LiteSignature))
		}
	}

	out := make([]byte, 0, liteHeaderSize+len(opts)+len(p.Payload))
	out = append(out, liteMagic, uint8(len(opts)))
	out = binary.LittleEndian.AppendUint16(out, uint16(len(p.Payload)))
	out = append(out, p.SourceType<<4|p.DestType&0xF, p.SourcePort, p.DestPort, p.FragmentID)
	out = binary.LittleEndian.AppendUint16(out, uint16(p.Type)&0xF|p.Flags<<4)
	out = binary.LittleEndian.AppendUint16(out, p.Sequence)
	out = append(out, opts...)
	return append(out, p.Payload...)
}

func pad16(b []byte) []byte {
	if len(b) == 16 {
		return b
	}
	out := make([]byte, 16)
	copy(out, b)
	return out
}

// ErrBadMagic means the data is not PRUDP-Lite at all.
var ErrBadMagic = errors.New("prudp: not a PRUDP-Lite packet")

// DecodePackets parses every complete packet in buf and returns the bytes of
// a trailing incomplete one. A console sends one packet per WebSocket message
// in practice, but nothing in the format promises it, and a packet split
// across two messages is the kind of thing that works until a login token
// gets long enough.
func DecodePackets(buf []byte) ([]*Packet, []byte, error) {
	var out []*Packet
	for len(buf) >= liteHeaderSize {
		if buf[0] != liteMagic {
			return out, nil, ErrBadMagic
		}
		optLen := int(buf[1])
		payloadLen := int(binary.LittleEndian.Uint16(buf[2:4]))
		total := liteHeaderSize + optLen + payloadLen
		if len(buf) < total {
			break
		}
		p := &Packet{
			SourceType: buf[4] >> 4,
			DestType:   buf[4] & 0xF,
			SourcePort: buf[5],
			DestPort:   buf[6],
			FragmentID: buf[7],
		}
		tf := binary.LittleEndian.Uint16(buf[8:10])
		p.Type = PacketType(tf & 0xF)
		p.Flags = tf >> 4
		p.Sequence = binary.LittleEndian.Uint16(buf[10:12])
		if err := p.decodeOptions(buf[liteHeaderSize : liteHeaderSize+optLen]); err != nil {
			return out, nil, err
		}
		p.Payload = append([]byte(nil), buf[liteHeaderSize+optLen:total]...)
		out = append(out, p)
		buf = buf[total:]
	}
	return out, buf, nil
}

func (p *Packet) decodeOptions(b []byte) error {
	for len(b) > 0 {
		if len(b) < 2 || len(b) < 2+int(b[1]) {
			return errors.New("prudp: truncated option")
		}
		id, v := b[0], b[2:2+int(b[1])]
		b = b[2+int(b[1]):]
		switch id {
		case optionSupport:
			if len(v) != 4 {
				return errors.New("prudp: support option is not 4 bytes")
			}
			n := binary.LittleEndian.Uint32(v)
			p.MinorVersion = uint8(n)
			p.SupportedFunctions = n >> 8
		case optionConnSig:
			p.ConnectionSignature = append([]byte(nil), v...)
		case optionLiteSignature:
			p.LiteSignature = append([]byte(nil), v...)
		case optionFragmentID, optionUnreliableSeq, optionMaxSubstream:
			// V1 options. Lite does not send them; tolerated rather than
			// refused, since ignoring them changes nothing.
		default:
			return fmt.Errorf("prudp: unknown option %#x", id)
		}
	}
	return nil
}

// liteSignature is what a client puts in its CONNECT: HMAC-MD5 keyed with
// MD5(access key) over MD5(access key) || the server's connection signature.
func liteSignature(accessKey string, connSig []byte) []byte {
	key := md5.Sum([]byte(accessKey))
	mac := hmac.New(md5.New, key[:])
	mac.Write(key[:])
	mac.Write(connSig)
	return mac.Sum(nil)
}
