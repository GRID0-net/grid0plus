package nex

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// NEX's simplified Kerberos: how the authentication server hands a console a
// ticket that the secure server can check without the two sharing anything
// but a key. Reference:
// https://github.com/kinnay/NintendoClients/wiki/Kerberos-Authentication
//
// The console never proves who it is to the secure server directly. It proves
// that it could decrypt what the authentication server sent it -- and the
// authentication server only sent that after GRID0+ verified the player's
// BAAS id token. So the chain of identity is: id token -> login -> ticket ->
// CONNECT, and the PID a secure connection acts as comes out of the ticket,
// never out of anything the console says afterwards.

// SessionKeySize is the Kerberos session key length on Switch.
const SessionKeySize = 32

// TicketLifetime is how old a ticket may be when it is presented. Two minutes,
// as the Kerberos page documents: long enough for a console to go from login
// to CONNECT, short enough that a ticket is useless to anyone who finds it later.
const TicketLifetime = 2 * time.Minute

// DeriveKey is the Switch ("new") key derivation: MD5(MD5(password) || PID as u64 LE).
func DeriveKey(password []byte, pid uint64) []byte {
	first := md5.Sum(password)
	buf := binary.LittleEndian.AppendUint64(first[:], pid)
	sum := md5.Sum(buf)
	return sum[:]
}

// kerberosEncrypt is RC4 with key, followed by an HMAC-MD5 of the ciphertext
// keyed with the same key.
func kerberosEncrypt(key, plain []byte) []byte {
	c, err := rc4.NewCipher(key)
	if err != nil {
		panic(err) // only for a key of length 0 or > 256, which callers never pass
	}
	out := make([]byte, len(plain), len(plain)+md5.Size)
	c.XORKeyStream(out, plain)
	mac := hmac.New(md5.New, key)
	mac.Write(out)
	return mac.Sum(out)
}

// ErrBadChecksum means the data was not encrypted with this key.
var ErrBadChecksum = errors.New("kerberos: checksum mismatch (wrong key)")

func kerberosDecrypt(key, data []byte) ([]byte, error) {
	if len(data) < md5.Size || len(key) == 0 {
		return nil, errors.New("kerberos: data too short")
	}
	body, sum := data[:len(data)-md5.Size], data[len(data)-md5.Size:]
	mac := hmac.New(md5.New, key)
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), sum) {
		return nil, ErrBadChecksum
	}
	c, err := rc4.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(body))
	c.XORKeyStream(out, body)
	return out, nil
}

// ServerTicket is the part only the secure server can read.
type ServerTicket struct {
	IssuedAt   DateTime
	Source     uint64
	SessionKey []byte
}

// Seal encrypts the ticket for the secure server. This is the "new" (version
// 1) internal ticket: a random key travels in the clear beside the ciphertext
// and is mixed into the server key, so no two tickets share an RC4 key.
func (t ServerTicket) Seal(serverKey []byte) []byte {
	w := NewWriter()
	w.DateTime(t.IssuedAt)
	w.PID(t.Source)
	w.Raw(t.SessionKey)

	ticketKey := make([]byte, 16)
	_, _ = rand.Read(ticketKey)
	final := md5.Sum(append(append([]byte{}, serverKey...), ticketKey...))

	out := NewWriter()
	out.Buffer(ticketKey)
	out.Buffer(kerberosEncrypt(final[:], w.Bytes()))
	return out.Bytes()
}

// OpenServerTicket decrypts what Seal produced.
func OpenServerTicket(serverKey, sealed []byte) (ServerTicket, error) {
	r := NewReader(sealed)
	ticketKey := r.Buffer()
	enc := r.Buffer()
	if r.Err() != nil {
		return ServerTicket{}, fmt.Errorf("kerberos: malformed ticket: %w", r.Err())
	}
	final := md5.Sum(append(append([]byte{}, serverKey...), ticketKey...))
	plain, err := kerberosDecrypt(final[:], enc)
	if err != nil {
		return ServerTicket{}, err
	}
	pr := NewReader(plain)
	t := ServerTicket{IssuedAt: pr.DateTime(), Source: pr.PID(),
		SessionKey: append([]byte(nil), pr.Raw(SessionKeySize)...)}
	if pr.Err() != nil {
		return ServerTicket{}, fmt.Errorf("kerberos: malformed ticket body: %w", pr.Err())
	}
	return t, nil
}

// ClientTicket is what the console decrypts: the session key, the principal
// it is for (the secure server), and the sealed ServerTicket to pass on.
type ClientTicket struct {
	SessionKey []byte
	Target     uint64
	Internal   []byte
}

// Seal encrypts the ticket with the console's source key.
func (t ClientTicket) Seal(sourceKey []byte) []byte {
	w := NewWriter()
	w.Raw(t.SessionKey)
	w.PID(t.Target)
	w.Buffer(t.Internal)
	return kerberosEncrypt(sourceKey, w.Bytes())
}

// OpenClientTicket decrypts what Seal produced (used by tests, which play the
// console).
func OpenClientTicket(sourceKey, sealed []byte) (ClientTicket, error) {
	plain, err := kerberosDecrypt(sourceKey, sealed)
	if err != nil {
		return ClientTicket{}, err
	}
	r := NewReader(plain)
	t := ClientTicket{SessionKey: append([]byte(nil), r.Raw(SessionKeySize)...), Target: r.PID(), Internal: r.Buffer()}
	return t, r.Err()
}

// IssueTicket mints a fresh session key and the ticket pair for source to
// connect to target, returning the ticket sealed for the console.
func IssueTicket(source, target uint64, sourceKey, serverKey []byte, now time.Time) []byte {
	session := make([]byte, SessionKeySize)
	_, _ = rand.Read(session)
	internal := ServerTicket{IssuedAt: NewDateTime(now), Source: source, SessionKey: session}.Seal(serverKey)
	return ClientTicket{SessionKey: session, Target: target, Internal: internal}.Seal(sourceKey)
}

// TicketAcceptor admits a CONNECT to the secure server: its payload must be a
// ticket this server's authentication half issued, under two minutes old,
// with a request encrypted under that ticket's session key and naming the
// same PID. Anything else is refused -- there is no path by which a secure
// connection exists without a principal.
//
// The CONNECT payload (PRUDP page, "Connection request"):
//
//	Buffer ticket (the sealed ServerTicket)
//	Buffer Kerberos(session key){ PID, u32 connection id, u32 check value }
//
// and the CONNECT/ACK carries Buffer{ u32 check value + 1 }.
func TicketAcceptor(serverKey []byte, now func() time.Time) AcceptFunc {
	if now == nil {
		now = time.Now
	}
	return func(payload []byte) (Identity, []byte, error) {
		if len(payload) == 0 {
			// The secure station URL must use the "prudps" scheme; with
			// "prudp" the console completes the handshake with an EMPTY
			// CONNECT -- no ticket at all -- and only fails later, once Pia
			// needs a principal. NextendoNetwork measured that (facts only);
			// here it is refused at once instead.
			return Identity{}, nil, errors.New("kerberos: CONNECT carries no ticket")
		}
		r := NewReader(payload)
		sealed := r.Buffer()
		request := r.Buffer()
		if r.Err() != nil {
			return Identity{}, nil, fmt.Errorf("kerberos: malformed connection request: %w", r.Err())
		}
		t, err := OpenServerTicket(serverKey, sealed)
		if err != nil {
			return Identity{}, nil, err
		}
		issued := t.IssuedAt.Time()
		// DateTime has one-second resolution, so allow that much in the future.
		if age := now().Sub(issued); age > TicketLifetime || age < -time.Second {
			return Identity{}, nil, fmt.Errorf("kerberos: ticket issued %s is outside its lifetime", issued.Format(time.RFC3339))
		}
		plain, err := kerberosDecrypt(t.SessionKey, request)
		if err != nil {
			return Identity{}, nil, fmt.Errorf("kerberos: connection request: %w", err)
		}
		rr := NewReader(plain)
		pid := rr.PID()
		_ = rr.U32() // the console's own connection id; this server assigns its own
		check := rr.U32()
		if rr.Err() != nil || rr.Remaining() != 0 {
			return Identity{}, nil, errors.New("kerberos: connection request has the wrong size")
		}
		if pid != t.Source {
			return Identity{}, nil, fmt.Errorf("kerberos: request names PID %d, ticket was issued to %d", pid, t.Source)
		}
		resp := NewWriter()
		resp.Buffer(binary.LittleEndian.AppendUint32(nil, check+1))
		return Identity{PID: t.Source, SessionKey: t.SessionKey}, resp.Bytes(), nil
	}
}

// ConnectRequest builds the CONNECT payload a console sends to the secure
// server (tests play the console with it).
func ConnectRequest(t ClientTicket, pid uint64, cid, check uint32) []byte {
	inner := NewWriter()
	inner.PID(pid)
	inner.U32(cid)
	inner.U32(check)
	w := NewWriter()
	w.Buffer(t.Internal)
	w.Buffer(kerberosEncrypt(t.SessionKey, inner.Bytes()))
	return w.Bytes()
}
