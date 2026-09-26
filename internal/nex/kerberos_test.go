package nex

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"
)

func TestSwitchKeyDerivationIsMD5OfMD5PasswordAndPID(t *testing.T) {
	pw := md5.Sum([]byte("pw"))
	want := md5.Sum(binary.LittleEndian.AppendUint64(pw[:], 42))
	if got := DeriveKey([]byte("pw"), 42); !bytes.Equal(got, want[:]) {
		t.Fatalf("derived %x", got)
	}
}

func TestATicketOpensOnlyWithTheKeyItWasSealedFor(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 16)
	sealed := ServerTicket{IssuedAt: Now(), Source: 7, SessionKey: bytes.Repeat([]byte{9}, 32)}.Seal(key)
	if _, err := OpenServerTicket(bytes.Repeat([]byte{2}, 16), sealed); err != ErrBadChecksum {
		t.Fatalf("opened with the wrong key: %v", err)
	}
	got, err := OpenServerTicket(key, sealed)
	if err != nil || got.Source != 7 || !bytes.Equal(got.SessionKey, bytes.Repeat([]byte{9}, 32)) {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

type console struct {
	pid    uint64
	ticket ClientTicket
}

func login(t *testing.T, serverKey []byte, pid uint64, at time.Time) console {
	t.Helper()
	source := bytes.Repeat([]byte{0x5A}, 32)
	sealed := IssueTicket(pid, 2, source, serverKey, at)
	ct, err := OpenClientTicket(source, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if ct.Target != 2 {
		t.Fatalf("ticket target %d", ct.Target)
	}
	return console{pid: pid, ticket: ct}
}

func TestAValidTicketAdmitsItsPrincipalAndAnswersTheCheck(t *testing.T) {
	serverKey := bytes.Repeat([]byte{3}, 16)
	now := time.Now()
	c := login(t, serverKey, 0x0123456789ABCDEF, now)

	accept := TicketAcceptor(serverKey, func() time.Time { return now.Add(30 * time.Second) })
	id, resp, err := accept(ConnectRequest(c.ticket, c.pid, 5, 0x10))
	if err != nil {
		t.Fatal(err)
	}
	if id.PID != 0x0123456789ABCDEF || !bytes.Equal(id.SessionKey, c.ticket.SessionKey) {
		t.Fatalf("identity %+v", id)
	}
	// Buffer{ u32 check+1 }: 04000000 11000000
	if got := hex.EncodeToString(resp); got != "0400000011000000" {
		t.Fatalf("CONNECT/ACK payload %s", got)
	}
}

func TestAnExpiredTicketIsRefused(t *testing.T) {
	serverKey := bytes.Repeat([]byte{3}, 16)
	issued := time.Now()
	c := login(t, serverKey, 1, issued)
	accept := TicketAcceptor(serverKey, func() time.Time { return issued.Add(TicketLifetime + 2*time.Second) })
	if _, _, err := accept(ConnectRequest(c.ticket, c.pid, 5, 0x10)); err == nil {
		t.Fatal("a ticket past its lifetime was accepted")
	}
}

func TestARequestNamingAnotherPIDIsRefused(t *testing.T) {
	// The ticket is the only source of a secure connection's identity. A
	// console holding its own ticket must not be able to claim another PID.
	serverKey := bytes.Repeat([]byte{3}, 16)
	c := login(t, serverKey, 100, time.Now())
	if _, _, err := TicketAcceptor(serverKey, nil)(ConnectRequest(c.ticket, 200, 5, 0x10)); err == nil {
		t.Fatal("a CONNECT claiming someone else's PID was accepted")
	}
}

func TestAConnectWithNoTicketIsRefused(t *testing.T) {
	if _, _, err := TicketAcceptor(bytes.Repeat([]byte{3}, 16), nil)(nil); err == nil {
		t.Fatal("an empty CONNECT reached the secure server")
	}
}

func TestATicketFromAnotherServerKeyIsRefused(t *testing.T) {
	// The secure key is random per process: a ticket issued before a restart
	// must not open after it.
	c := login(t, bytes.Repeat([]byte{3}, 16), 1, time.Now())
	if _, _, err := TicketAcceptor(bytes.Repeat([]byte{4}, 16), nil)(ConnectRequest(c.ticket, 1, 5, 0x10)); err == nil {
		t.Fatal("a ticket sealed for another key was accepted")
	}
}
