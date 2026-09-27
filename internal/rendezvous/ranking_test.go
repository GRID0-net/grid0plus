package rendezvous_test

import (
	"testing"

	"github.com/n-popescu/grid0plus/internal/nex"
	"github.com/n-popescu/grid0plus/internal/rendezvous"
	"github.com/n-popescu/grid0plus/internal/rendezvous/rvtest"
)

func uploadScore(t *testing.T, p *rvtest.Player, category, score uint32, updateMode uint8) {
	t.Helper()
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) {
		w.U32(category)
		w.U32(score)
		w.U8(0)
		w.U8(updateMode)
		nex.WriteList(w, []uint8{1, 2}, (*nex.Writer).U8)
		w.U64(9)
	})
	w.U64(p.PID) // unique id
	p.MustCall(t, rendezvous.ProtocolRanking, 1, w.Bytes())
}

type entry struct {
	pid          uint64
	order, score uint32
	common       []byte
}

func getRanking(t *testing.T, p *rvtest.Player, mode uint8, category uint32) ([]entry, uint32, *nex.Response) {
	t.Helper()
	w := nex.NewWriter()
	w.U8(mode)
	w.U32(category)
	w.Struct(0, func(w *nex.Writer) {
		w.U8(0)
		w.U8(255)
		w.U8(0)
		w.U8(0)
		w.U32(0)
		w.U8(10)
	})
	w.U64(0)
	w.PID(0)
	resp := p.Call(t, rendezvous.ProtocolRanking, 9, w.Bytes())
	if !resp.OK() {
		return nil, 0, resp
	}
	r := nex.NewReader(resp.Body)
	var list []entry
	var total uint32
	r.Struct(func(r *nex.Reader, _ uint8) {
		list = nex.ReadList(r, func(r *nex.Reader) entry {
			var e entry
			r.Struct(func(r *nex.Reader, _ uint8) {
				e.pid = r.PID()
				_ = r.U64()
				e.order = r.U32()
				_ = r.U32()
				e.score = r.U32()
				_ = nex.ReadList(r, (*nex.Reader).U8)
				_ = r.U64()
				e.common = r.Buffer()
			})
			return e
		})
		total = r.U32()
		_ = r.DateTime()
	})
	if r.Err() != nil || r.Remaining() != 0 {
		t.Fatalf("RankingResult: %v, %d bytes left", r.Err(), r.Remaining())
	}
	return list, total, resp
}

func TestAnEmptyRankingIsNotFound(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	_, _, resp := getRanking(t, e.Login(), 0, 1)
	if resp.OK() || resp.Result != nex.ResultRankingNotFound|nex.ErrorMask {
		t.Fatalf("answered %#x", resp.Result)
	}
}

func TestScoresRankHighestFirstAndTiesShareARank(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	a, b, c := e.Login(), e.Login(), e.Login()
	uploadScore(t, a, 1, 100, 0)
	uploadScore(t, b, 1, 300, 0)
	uploadScore(t, c, 1, 100, 0)
	uploadScore(t, a, 2, 999, 0) // another category

	list, total, _ := getRanking(t, a, 0, 1)
	if total != 3 || len(list) != 3 || list[0].pid != b.PID || list[0].order != 1 ||
		list[1].order != 2 || list[2].order != 2 {
		t.Fatalf("ranking %+v total %d", list, total)
	}
}

func TestAWorseScoreDoesNotReplaceABetterOne(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	a := e.Login()
	uploadScore(t, a, 1, 500, 0)
	uploadScore(t, a, 1, 200, 0)
	if list, _, _ := getRanking(t, a, 4, 1); len(list) != 1 || list[0].score != 500 {
		t.Fatalf("own ranking %+v", list)
	}
	uploadScore(t, a, 1, 200, 1) // replace mode
	if list, _, _ := getRanking(t, a, 4, 1); list[0].score != 200 {
		t.Fatalf("replace mode kept %+v", list)
	}
}

func TestCommonDataRidesAlongWithTheRanking(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	a := e.Login()
	w := nex.NewWriter()
	w.Buffer([]byte("mii"))
	w.U64(a.PID)
	a.MustCall(t, rendezvous.ProtocolRanking, 4, w.Bytes())
	uploadScore(t, a, 1, 5, 0)
	if list, _, _ := getRanking(t, a, 0, 1); string(list[0].common) != "mii" {
		t.Fatalf("common data %q", list[0].common)
	}
	w = nex.NewWriter()
	w.U64(a.PID)
	if got := a.MustCall(t, rendezvous.ProtocolRanking, 6, w.Bytes()).Buffer(); string(got) != "mii" {
		t.Fatalf("GetCommonData %q", got)
	}
}
