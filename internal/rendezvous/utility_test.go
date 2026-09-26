package rendezvous_test

import (
	"encoding/hex"
	"testing"

	"github.com/n-popescu/switchnet/internal/nex"
	"github.com/n-popescu/switchnet/internal/rendezvous"
	"github.com/n-popescu/switchnet/internal/rendezvous/rvtest"
)

func uniqueIDInfo(id, password uint64) []byte {
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) { w.U64(id); w.U64(password) })
	return w.Bytes()
}

func TestAUniqueIDIsHandedOutThenClaimedByItsOwnerOnly(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	owner, other := e.Login(), e.Login()

	r := owner.MustCall(t, rendezvous.ProtocolUtility, 2, nil)
	u := rendezvous.ReadUniqueIDInfo(r)
	if u.ID == 0 || u.Password == 0 || r.Remaining() != 0 {
		t.Fatalf("AcquireNexUniqueIdWithPassword gave %+v", u)
	}

	// Nobody else can claim it, and nor can the owner with the wrong password.
	if resp := other.Call(t, rendezvous.ProtocolUtility, 3, uniqueIDInfo(u.ID, u.Password)); resp.OK() {
		t.Fatal("another player claimed the id")
	}
	if resp := owner.Call(t, rendezvous.ProtocolUtility, 3, uniqueIDInfo(u.ID, u.Password+1)); resp.OK() {
		t.Fatal("claimed with the wrong password")
	}

	// Before the claim, GetAssociated answers zeros ("acquire one").
	got := rendezvous.ReadUniqueIDInfo(owner.MustCall(t, rendezvous.ProtocolUtility, 5, nil))
	if got.ID != 0 {
		t.Fatalf("an unclaimed id was reported associated: %+v", got)
	}
	owner.MustCall(t, rendezvous.ProtocolUtility, 3, uniqueIDInfo(u.ID, u.Password))
	got = rendezvous.ReadUniqueIDInfo(owner.MustCall(t, rendezvous.ProtocolUtility, 5, nil))
	if got.ID != u.ID || got.Password != u.Password {
		t.Fatalf("GetAssociated = %+v, want %+v", got, u)
	}
	list := nex.ReadList(owner.MustCall(t, rendezvous.ProtocolUtility, 6, nil), rendezvous.ReadUniqueIDInfo)
	if len(list) != 1 || list[0].ID != u.ID {
		t.Fatalf("GetAssociateds = %+v", list)
	}
	if list := nex.ReadList(other.MustCall(t, rendezvous.ProtocolUtility, 6, nil), rendezvous.ReadUniqueIDInfo); len(list) != 0 {
		t.Fatalf("the other player sees %+v", list)
	}
}

func TestAPlainUniqueIDIsABareU64(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	p := e.Login()
	r := p.MustCall(t, rendezvous.ProtocolUtility, 1, nil)
	if id := r.U64(); id == 0 || r.Remaining() != 0 {
		t.Fatalf("AcquireNexUniqueId gave %d with %d bytes left", id, r.Remaining())
	}
}

func TestSettingsAreAnEmptyMap(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	p := e.Login()
	w := nex.NewWriter()
	w.U32(0)
	for _, m := range []uint32{7, 8} {
		resp := p.Call(t, rendezvous.ProtocolUtility, m, w.Bytes())
		if !resp.OK() || hex.EncodeToString(resp.Body) != "00000000" {
			t.Fatalf("method %d answered %x", m, resp.Body)
		}
	}
}
