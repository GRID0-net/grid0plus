// Package dns is a DNS server that redirects Nintendo's hostnames to SwitchNet
// and forwards everything else upstream.
//
// # Why this exists
//
// The usual way to point a Switch at a custom server is Atmosphère's DNS-MITM: a
// hosts file on the SD card, plus a firmware-build-id-gated exefs patch to make
// the console accept the certificate. That works, but it is fragile in a specific
// way — the patch silently does nothing when it does not match the console's
// firmware, and the hosts file is rewritten from scratch by the tool that manages
// it.
//
// Redirection does not have to happen on the console at all. A Switch resolves
// hostnames through whatever DNS server its network settings give it, and that
// setting exists in stock firmware — or can be handed out by the router over
// DHCP, in which case the console needs no configuration whatsoever.
//
// So this server replaces the hosts file entirely. What it cannot replace is
// certificate trust; see switch/README.md for that half.
//
// # Only the wire format we answer is parsed
//
// A query for a name SwitchNet does not own is relayed to the upstream resolver
// as opaque bytes and the reply relayed back untouched. So the parser only has to
// handle a header and a question section, and never a resource record from
// somebody else's zone. That is what makes hand-rolling this reasonable rather
// than reckless.
package dns

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Wire format constants.
const (
	headerLen = 12

	// maxUDPMessage is the classic DNS limit. Every answer this server
	// synthesises is one A record, so nothing comes close.
	maxUDPMessage = 512
	// maxTCPMessage bounds a TCP query, so a hostile client cannot make the
	// server allocate. 64 KiB is the protocol maximum.
	maxTCPMessage = 65535

	maxLabelLen = 63
	maxNameLen  = 255
)

// Record types, only the ones this server reasons about.
const (
	TypeA     = 1
	TypeCNAME = 5
	TypeAAAA  = 28
	// TypeHTTPS is queried by newer clients alongside A. Answering NODATA stops
	// a client using it to reach the real host.
	TypeHTTPS = 65
	TypeANY   = 255
)

// Class IN.
const classIN = 1

// Response codes.
const (
	rcodeNoError  = 0
	rcodeFormErr  = 1
	rcodeservFail = 2
	rcodeNXDomain = 3
	rcodeRefused  = 5
)

// ErrMalformed means a message could not be parsed. Malformed queries are
// dropped rather than answered: something that is not a DNS client is talking to
// the port.
var ErrMalformed = errors.New("dns: malformed message")

// Question is the parsed question section of a query.
type Question struct {
	// Name is the queried name, lower-cased and without the trailing dot.
	Name string
	Type uint16
	// Class is normally classIN.
	Class uint16
	// end is the offset just past the question section, so a response can copy
	// the question bytes verbatim rather than re-encoding them.
	end int
}

// header is the parsed 12-byte message header.
type header struct {
	id      uint16
	flags   uint16
	qdCount uint16
}

func (h header) recursionDesired() bool { return h.flags&0x0100 != 0 }

// parseQuery reads the header and the single question of a query.
//
// It returns ErrMalformed for anything it will not answer, which the caller
// treats as "forward it" or "drop it" rather than trying to interpret further.
func parseQuery(msg []byte) (header, Question, error) {
	if len(msg) < headerLen {
		return header{}, Question{}, fmt.Errorf("%w: %d bytes, need at least %d",
			ErrMalformed, len(msg), headerLen)
	}

	h := header{
		id:      binary.BigEndian.Uint16(msg[0:2]),
		flags:   binary.BigEndian.Uint16(msg[2:4]),
		qdCount: binary.BigEndian.Uint16(msg[4:6]),
	}

	// A response, not a query.
	if h.flags&0x8000 != 0 {
		return h, Question{}, fmt.Errorf("%w: QR bit set on a query", ErrMalformed)
	}
	// Standard query only. Anything else (IQUERY, STATUS, UPDATE) is forwarded.
	if opcode := (h.flags >> 11) & 0xf; opcode != 0 {
		return h, Question{}, fmt.Errorf("%w: opcode %d", ErrMalformed, opcode)
	}
	// Exactly one question. Multi-question queries are legal on paper and used by
	// nothing; forwarding them is safer than guessing which one to answer.
	if h.qdCount != 1 {
		return h, Question{}, fmt.Errorf("%w: qdcount %d", ErrMalformed, h.qdCount)
	}

	name, offset, err := parseName(msg, headerLen)
	if err != nil {
		return h, Question{}, err
	}
	if offset+4 > len(msg) {
		return h, Question{}, fmt.Errorf("%w: question truncated", ErrMalformed)
	}

	q := Question{
		Name:  name,
		Type:  binary.BigEndian.Uint16(msg[offset : offset+2]),
		Class: binary.BigEndian.Uint16(msg[offset+2 : offset+4]),
		end:   offset + 4,
	}
	return h, q, nil
}

// parseName reads a QNAME.
//
// Compression pointers are rejected rather than followed. A QNAME in a question
// is never compressed — there is nothing before it to point at — so a pointer
// here means either a broken client or something trying to confuse the parser.
func parseName(msg []byte, offset int) (string, int, error) {
	var sb strings.Builder
	total := 0

	for {
		if offset >= len(msg) {
			return "", 0, fmt.Errorf("%w: name runs past the message", ErrMalformed)
		}
		length := int(msg[offset])
		offset++

		if length == 0 {
			return strings.ToLower(sb.String()), offset, nil
		}
		if length&0xc0 != 0 {
			return "", 0, fmt.Errorf("%w: compression pointer in a question name", ErrMalformed)
		}
		if length > maxLabelLen {
			return "", 0, fmt.Errorf("%w: label of %d bytes", ErrMalformed, length)
		}

		total += length + 1
		if total > maxNameLen {
			return "", 0, fmt.Errorf("%w: name longer than %d bytes", ErrMalformed, maxNameLen)
		}
		if offset+length > len(msg) {
			return "", 0, fmt.Errorf("%w: label runs past the message", ErrMalformed)
		}

		if sb.Len() > 0 {
			sb.WriteByte('.')
		}
		sb.Write(msg[offset : offset+length])
		offset += length
	}
}

// answer builds a reply to a query, containing zero or one A record.
//
// A nil ip produces NODATA: NOERROR with no answers. That is the right response
// for an AAAA query on a name this server owns — the name exists, it just has no
// IPv6 address here.
//
// Getting that wrong is the subtle failure this design has to avoid. If AAAA
// queries were forwarded upstream while A queries were answered locally, a
// console that prefers IPv6 would get Nintendo's real address and talk straight
// past SwitchNet, intermittently, depending on its network.
func answer(msg []byte, h header, q Question, ip net.IP, ttl uint32) []byte {
	out := make([]byte, 0, q.end+16)

	flags := uint16(0x8000) // QR: this is a response.
	flags |= 0x0400         // AA: authoritative, since we are the source of truth here.
	if h.recursionDesired() {
		flags |= 0x0100 // RD, echoed back.
		flags |= 0x0080 // RA: recursion is available, via the upstream.
	}

	var ancount uint16
	var rdata []byte
	if ip != nil {
		if v4 := ip.To4(); v4 != nil {
			rdata = v4
			ancount = 1
		}
	}

	head := make([]byte, headerLen)
	binary.BigEndian.PutUint16(head[0:2], h.id)
	binary.BigEndian.PutUint16(head[2:4], flags|rcodeNoError)
	binary.BigEndian.PutUint16(head[4:6], 1) // qdcount
	binary.BigEndian.PutUint16(head[6:8], ancount)
	// No authority and no additional. A NODATA response would normally carry an
	// SOA for negative caching; the console is a stub resolver talking straight
	// to us, so there is nothing downstream that would cache it.
	binary.BigEndian.PutUint16(head[8:10], 0)
	binary.BigEndian.PutUint16(head[10:12], 0)
	out = append(out, head...)

	// The question, copied byte for byte. Re-encoding it risks answering a
	// different name from the one that was asked.
	out = append(out, msg[headerLen:q.end]...)

	if ancount == 1 {
		rr := make([]byte, 0, 16)
		// A compression pointer to offset 12, the start of the question's name.
		rr = append(rr, 0xc0, 0x0c)
		rr = binary.BigEndian.AppendUint16(rr, TypeA)
		rr = binary.BigEndian.AppendUint16(rr, classIN)
		rr = binary.BigEndian.AppendUint32(rr, ttl)
		rr = binary.BigEndian.AppendUint16(rr, uint16(len(rdata)))
		rr = append(rr, rdata...)
		out = append(out, rr...)
	}

	return out
}

// errorResponse builds a reply carrying only an rcode.
func errorResponse(msg []byte, h header, q Question, rcode uint16) []byte {
	out := make([]byte, 0, q.end)

	flags := uint16(0x8000)
	if h.recursionDesired() {
		flags |= 0x0100 | 0x0080
	}

	head := make([]byte, headerLen)
	binary.BigEndian.PutUint16(head[0:2], h.id)
	binary.BigEndian.PutUint16(head[2:4], flags|rcode)
	binary.BigEndian.PutUint16(head[4:6], 1)
	out = append(out, head...)
	out = append(out, msg[headerLen:q.end]...)
	return out
}

// wantsAddress reports whether a query type should get an A record when the name
// resolves to one.
func wantsAddress(qtype uint16) bool {
	return qtype == TypeA || qtype == TypeANY
}
