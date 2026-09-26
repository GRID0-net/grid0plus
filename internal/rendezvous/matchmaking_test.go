package rendezvous_test

import (
	"context"
	"testing"
	"time"

	"github.com/n-popescu/switchnet/internal/nex"
	"github.com/n-popescu/switchnet/internal/rendezvous"
	"github.com/n-popescu/switchnet/internal/rendezvous/rvtest"
)

func template(mode uint32, maxP uint16, flags uint32, password string) *rendezvous.MatchmakeSession {
	return &rendezvous.MatchmakeSession{
		Gathering: rendezvous.Gathering{MinParticipants: 2, MaxParticipants: maxP, Flags: flags},
		GameMode:  mode, Attribs: []uint32{0, 0, 0, 0, 0, 0}, OpenParticipation: true,
		MatchmakeSystemType: 1, UserPassword: password,
	}
}

func autoParam(s *rendezvous.MatchmakeSession, additional []uint64, criteria []string) []byte {
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) {
		rendezvous.WriteMatchmakeSession(w, s)
		nex.WriteList(w, additional, (*nex.Writer).PID)
		w.U32(0)
		w.U32(0)
		w.String("hello")
		w.U16(1)
		if criteria == nil {
			w.U32(0)
		} else {
			w.U32(1)
			w.Struct(0, func(w *nex.Writer) {
				nex.WriteList(w, criteria, (*nex.Writer).String)
				w.String("")
				w.String("")
				w.String("")
				w.String("")
				w.Bool(true)
				w.Bool(true)
				w.Bool(false)
				w.U32(0)
				w.U16(0)
				w.Struct(0, func(w *nex.Writer) { w.U32(0) })
				w.Bool(true)
				w.Bool(true)
				w.U32(0)
				w.String("")
				w.Struct(0, func(w *nex.Writer) { w.U32(0); w.U32(0) })
			})
		}
		w.U32(0)                                      // target gids
		w.Struct(0, func(w *nex.Writer) { w.U32(0) }) // block list
	})
	return w.Bytes()
}

func auto(t *testing.T, p *rvtest.Player, s *rendezvous.MatchmakeSession) *rendezvous.MatchmakeSession {
	t.Helper()
	r := p.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 40, autoParam(s, nil, nil))
	got := rendezvous.ReadMatchmakeSession(r)
	if r.Err() != nil || r.Remaining() != 0 {
		t.Fatalf("AutoMatchmakeWithParam response: err %v, %d bytes left", r.Err(), r.Remaining())
	}
	return got
}

// notifications collects the Participate events a console receives until
// none arrives for a moment.
func notifications(t *testing.T, p *rvtest.Player) []rendezvous.NotificationEvent {
	t.Helper()
	var out []rendezvous.NotificationEvent
	for {
		n, err := p.Secure.Notification(300 * time.Millisecond)
		if err != nil {
			return out
		}
		if n.Protocol != rendezvous.ProtocolNotifications || n.Method != 1 {
			t.Fatalf("unexpected server call %#x.%d", n.Protocol, n.Method)
		}
		out = append(out, rendezvous.ReadNotificationEvent(nex.NewReader(n.Body)))
	}
}

func TestTwoPlayersMatchIntoOneLobbyAndEachIsToldAboutTheOther(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	host, joiner := e.Login(), e.Login()

	a := auto(t, host, template(2, 8, 0, ""))
	if a.Owner != host.PID || a.Host != host.PID || len(a.SessionKey) != 32 || a.ParticipationCount != 1 {
		t.Fatalf("created session %+v", a.Gathering)
	}
	// A host alone in a new lobby is told about itself: Pia waits for that.
	if n := notifications(t, host); len(n) != 1 || n[0].Type != rendezvous.NotifyParticipate || n[0].Param2 != host.PID ||
		n[0].Param1 != uint64(a.ID) || n[0].StrParam != "hello" || n[0].Param3 != 1 {
		t.Fatalf("host's own notification: %+v", n)
	}

	b := auto(t, joiner, template(2, 8, 0, ""))
	if b.ID != a.ID || b.ParticipationCount != 2 || string(b.SessionKey) != string(a.SessionKey) {
		t.Fatalf("joiner landed in %d (%d players), host is in %d", b.ID, b.ParticipationCount, a.ID)
	}
	hostSaw := notifications(t, host)
	if len(hostSaw) != 1 || hostSaw[0].Param2 != joiner.PID || hostSaw[0].Param3 != 2 {
		t.Fatalf("host was told %+v", hostSaw)
	}
	joinerSaw := notifications(t, joiner)
	var self, recap bool
	for _, n := range joinerSaw {
		self = self || n.Param2 == joiner.PID
		recap = recap || n.Param2 == host.PID
	}
	if !self || !recap {
		t.Fatalf("joiner was told %+v: needs itself and a recap of the host", joinerSaw)
	}
}

func TestDifferentGameModesDoNotMatch(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	a := auto(t, e.Login(), template(2, 8, 0, ""))
	b := auto(t, e.Login(), template(3, 8, 0, ""))
	if a.ID == b.ID {
		t.Fatal("a Turf War and a Ranked lobby were merged")
	}
}

func TestSearchCriteriaFilterOnAttributes(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	tmpl := template(2, 8, 0, "")
	tmpl.Attribs = []uint32{10, 0, 5, 0, 0, 0}
	a := auto(t, e.Login(), tmpl)

	p := e.Login()
	// Attribute 2 must be 6..9: the lobby has 5.
	r := p.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 40,
		autoParam(template(2, 8, 0, ""), nil, []string{"10", "", "6,9", "", "", ""}))
	if got := rendezvous.ReadMatchmakeSession(r); got.ID == a.ID {
		t.Fatal("criteria excluding the lobby still matched it")
	}
	q := e.Login()
	r = q.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 40,
		autoParam(template(2, 8, 0, ""), nil, []string{"10", "999", "1,5", "", "", ""}))
	if got := rendezvous.ReadMatchmakeSession(r); got.ID != a.ID {
		t.Fatal("criteria matching the lobby did not find it (attribute 1 must be ignored)")
	}
}

func TestAClosedLobbyIsNotMatchedAndClosingStartsTheMatch(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	var started []rendezvous.GatheringInfo
	e.Server.Matchmaker().OnMatchStart(func(g rendezvous.GatheringInfo) { started = append(started, g) })
	host, other := e.Login(), e.Login()
	a := auto(t, host, template(2, 8, 0, ""))

	w := nex.NewWriter()
	w.U32(a.ID)
	if resp := other.Call(t, rendezvous.ProtocolMatchmakeExtension, 1, w.Bytes()); resp.OK() {
		t.Fatal("a stranger closed someone else's lobby")
	}
	host.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 1, w.Bytes())
	host.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 1, w.Bytes())
	if len(started) != 1 || started[0].ID != a.ID {
		t.Fatalf("match start hook ran %d times", len(started))
	}
	if b := auto(t, other, template(2, 8, 0, "")); b.ID == a.ID {
		t.Fatal("a closed lobby took a new player")
	}
}

func createPrivate(t *testing.T, p *rvtest.Player, password string) *rendezvous.MatchmakeSession {
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) {
		rendezvous.WriteMatchmakeSession(w, template(5, 2, 0, password))
		w.U32(0)
		w.U32(0)
		w.U32(0)
		w.String("")
		w.U16(1)
	})
	return rendezvous.ReadMatchmakeSession(p.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 38, w.Bytes()))
}

func joinParam(gid uint32, password string) []byte {
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) {
		w.U32(gid)
		w.U32(0)
		w.U32(0)
		w.U32(0)
		w.U8(0)
		w.String(password)
		w.String("")
		w.String("hi")
		w.U16(1)
		w.U16(0)
		w.Struct(0, func(w *nex.Writer) { w.U32(0) })
	})
	return w.Bytes()
}

func TestAPrivateLobbyNeedsItsPasswordAndHasALimit(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	owner := e.Login()
	s := createPrivate(t, owner, "1234")
	if !s.UserPasswordEnabled || s.UserPassword != "" {
		t.Fatalf("private session %+v leaks or loses its password", s)
	}
	if b := auto(t, e.Login(), template(5, 2, 0, "")); b.ID == s.ID {
		t.Fatal("auto-matchmake entered a password-protected lobby")
	}
	guest := e.Login()
	if resp := guest.Call(t, rendezvous.ProtocolMatchmakeExtension, 39, joinParam(s.ID, "0000")); resp.OK() ||
		resp.Result != nex.ResultPermissionDenied|nex.ErrorMask {
		t.Fatalf("wrong password answered %#x", resp.Result)
	}
	joined := rendezvous.ReadMatchmakeSession(guest.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 39, joinParam(s.ID, "1234")))
	if joined.ID != s.ID || len(joined.SessionKey) != 32 {
		t.Fatalf("join gave %+v", joined.Gathering)
	}
	if resp := e.Login().Call(t, rendezvous.ProtocolMatchmakeExtension, 39, joinParam(s.ID, "1234")); resp.Result != nex.ResultSessionFull|nex.ErrorMask {
		t.Fatalf("a full lobby answered %#x", resp.Result)
	}
}

func TestReadingASessionYouAreNotInHidesItsKey(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	a := auto(t, e.Login(), template(2, 8, 0, ""))
	w := nex.NewWriter()
	w.U32(a.ID)
	got := rendezvous.ReadMatchmakeSession(e.Login().MustCall(t, rendezvous.ProtocolMatchmakeExtension, 49, w.Bytes()))
	if got.ID != a.ID || len(got.SessionKey) != 0 {
		t.Fatalf("an outsider read key %x", got.SessionKey)
	}
}

func TestWhenTheOwnerLeavesALobbyThatDidNotAskToMigrateItIsGone(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	host, joiner := e.Login(), e.Login()
	a := auto(t, host, template(2, 8, 0, ""))
	auto(t, joiner, template(2, 8, 0, ""))
	notifications(t, joiner)

	_ = host.Secure.Close()
	saw := notifications(t, joiner)
	if len(saw) != 1 || saw[0].Type != rendezvous.NotifyGatheringUnregisterd || saw[0].Param1 != uint64(a.ID) {
		t.Fatalf("joiner was told %+v", saw)
	}
	if gs := e.Server.Matchmaker().Gatherings(); len(gs) != 0 {
		t.Fatalf("gatherings left: %+v", gs)
	}
}

func TestWhenTheOwnerLeavesAMigratingLobbyTheNextPlayerTakesOver(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	host, joiner := e.Login(), e.Login()
	a := auto(t, host, template(2, 8, rendezvous.FlagMigrateOwner, ""))
	auto(t, joiner, template(2, 8, rendezvous.FlagMigrateOwner, ""))
	notifications(t, joiner)

	_ = host.Secure.Close()
	saw := notifications(t, joiner)
	types := map[uint32]rendezvous.NotificationEvent{}
	for _, n := range saw {
		types[n.Type] = n
	}
	if types[rendezvous.NotifyOwnershipChanged].Param2 != joiner.PID {
		t.Fatalf("ownership: %+v", saw)
	}
	if _, ok := types[rendezvous.NotifyHostChanged]; !ok {
		t.Fatalf("no host change: %+v", saw)
	}
	if left := types[rendezvous.NotifyParticipantLeft]; left.Param2 != host.PID {
		t.Fatalf("no departure: %+v", saw)
	}
	g, ok := e.Server.Matchmaker().GatheringOf(joiner.PID)
	if !ok || g.ID != a.ID || g.Owner != joiner.PID || g.Host != joiner.PID || len(g.Participants) != 1 {
		t.Fatalf("after migration: %+v", g)
	}
}

func TestStrictNATsAreNotPutTogether(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	report := func(p *rvtest.Player, mapping uint32) {
		w := nex.NewWriter()
		w.U32(mapping)
		w.U32(2)
		w.U32(30)
		p.MustCall(t, rendezvous.ProtocolNATTraversal, 5, w.Bytes())
	}
	a, b, c := e.Login(), e.Login(), e.Login()
	report(a, 2)
	report(b, 2)
	report(c, 1)
	ga := auto(t, a, template(2, 8, 0, ""))
	if gb := auto(t, b, template(2, 8, 0, "")); gb.ID == ga.ID {
		t.Fatal("two strict NATs were matched")
	}
	// c is moderate (independent mapping, dependent filtering): still no
	// match for a strict lobby.
	if gc := auto(t, c, template(2, 8, 0, "")); gc.ID == ga.ID {
		t.Fatal("a moderate NAT joined a strict one")
	}
}

func TestAFriendCanFindTheLobbyAFriendIsInButAStrangerCannot(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	host, friend, stranger := e.Login(), e.Login(), e.Login()
	if err := e.Store.AddFriendship(context.Background(), host.UserID, friend.UserID, time.Now()); err != nil {
		t.Fatal(err)
	}
	a := auto(t, host, template(2, 8, 0, ""))

	ask := func(p *rvtest.Player) []uint64 {
		w := nex.NewWriter()
		nex.WriteList(w, []uint64{host.PID}, (*nex.Writer).PID)
		w.Bool(false)
		r := p.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 31, w.Bytes())
		var gids []uint64
		for _, s := range nex.ReadList(r, func(r *nex.Reader) [2]uint64 {
			var x [2]uint64
			r.Struct(func(r *nex.Reader, _ uint8) { x[0] = r.PID(); x[1] = uint64(r.U32()); _ = r.U32(); _ = r.U32() })
			return x
		}) {
			gids = append(gids, s[1])
		}
		return gids
	}
	if got := ask(friend); len(got) != 1 || got[0] != uint64(a.ID) {
		t.Fatalf("friend got %v", got)
	}
	if got := ask(stranger); len(got) != 0 {
		t.Fatalf("stranger got %v", got)
	}
}

func TestGetSessionURLsWaitsForTheHostsUpdatedStation(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	host, joiner := e.Login(), e.Login()
	host.Register(t, "prudp:/address=10.0.0.9;port=5000;type=0")
	a := auto(t, host, template(2, 8, 0, ""))
	auto(t, joiner, template(2, 8, 0, ""))

	go func() {
		time.Sleep(300 * time.Millisecond)
		w := nex.NewWriter()
		w.String("prudp:/address=10.0.0.9;port=5000;type=0")
		w.String("prudp:/address=10.0.0.9;port=5000;CID=7;type=0")
		_, _ = host.Secure.Call(rendezvous.ProtocolSecureConnection, 7, w.Bytes())
	}()
	w := nex.NewWriter()
	w.U32(a.ID)
	start := time.Now()
	r := joiner.MustCall(t, rendezvous.ProtocolMatchMaking, 41, w.Bytes())
	urls := nex.ReadList(r, (*nex.Reader).StationURL)
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("answered before the host re-reported its station")
	}
	if len(urls) == 0 || urls[0].Get("CID") != "7" {
		t.Fatalf("urls %v", urls)
	}
}

func TestFriendNotificationDataReachesFriendsOnly(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	a, friend, stranger := e.Login(), e.Login(), e.Login()
	if err := e.Store.AddFriendship(context.Background(), a.UserID, friend.UserID, time.Now()); err != nil {
		t.Fatal(err)
	}
	w := nex.NewWriter()
	w.U32(101)
	w.U64(55)
	w.U64(66)
	w.String("room")
	a.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 9, w.Bytes())

	ask := func(p *rvtest.Player) []rendezvous.NotificationEvent {
		w := nex.NewWriter()
		nex.WriteList(w, []uint32{101}, (*nex.Writer).U32)
		return nex.ReadList(p.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 13, w.Bytes()), rendezvous.ReadNotificationEvent)
	}
	if got := ask(friend); len(got) != 1 || got[0].Source != a.PID || got[0].Param1 != 55 || got[0].StrParam != "room" {
		t.Fatalf("friend got %+v", got)
	}
	if got := ask(stranger); len(got) != 0 {
		t.Fatalf("stranger got %+v", got)
	}
}

func TestUpdatingAGatheringThatIsAlreadyGoneStillSucceeds(t *testing.T) {
	// The end of a Salmon Run shift: the last console out updates a
	// gathering everyone else already left.
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	p := e.Login()
	w := nex.NewWriter()
	w.Struct(0, func(w *nex.Writer) {
		w.U32(999)
		w.U32(0x2)
		w.U32(0)
		w.Bool(false)
		w.Buffer(nil)
		w.U8(0)
		w.Struct(0, func(w *nex.Writer) { w.U32(0) })
		w.DateTime(0)
		w.String("")
		w.U32(0)
		w.String("")
		w.U16(0)
		w.U16(0)
		w.U32(0)
		w.U32(0)
		w.U32(0)
		w.String("")
	})
	p.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 44, w.Bytes())
}

func TestAGameAttributeIsRestampedInPlace(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	host := e.Login()
	a := auto(t, host, template(2, 8, 0, ""))
	w := nex.NewWriter()
	w.U32(a.ID)
	w.U32(3)
	w.U32(77)
	host.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 8, w.Bytes())
	g, _ := e.Server.Matchmaker().GatheringOf(host.PID)
	if g.Attribs[3] != 77 {
		t.Fatalf("attribs %v", g.Attribs)
	}
}

func TestASquadJoinsTogetherAndTheOthersAreToldWhere(t *testing.T) {
	e := rvtest.New(t, rvtest.DefaultTitle, nil)
	leader, mate := e.Login(), e.Login()
	r := leader.MustCall(t, rendezvous.ProtocolMatchmakeExtension, 40, autoParam(template(2, 8, 0, ""), []uint64{mate.PID}, nil))
	s := rendezvous.ReadMatchmakeSession(r)
	if s.ParticipationCount != 2 {
		t.Fatalf("squad of two made a lobby of %d", s.ParticipationCount)
	}
	var told bool
	for _, n := range notifications(t, mate) {
		told = told || (n.Type == rendezvous.NotifyAddedToGathering && n.Param1 == uint64(s.ID))
	}
	if !told {
		t.Fatal("the squad mate was not told which lobby they are in")
	}
}
