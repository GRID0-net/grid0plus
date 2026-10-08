package gamesync

import (
	"testing"
	"time"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

func TestSessionPhaseUsesBattleEventsRatherThanTheRoomsAge(t *testing.T) {
	now := time.Unix(1000, 0)
	s := &Server{docs: newStoreSet(func() time.Time { return now })}
	st := s.docs.in("room")
	write := func(name string, fields map[string]*commonpb.Value) {
		t.Helper()
		now = now.Add(time.Second)
		if _, _, err := st.apply([]*syncpb.WriteOperation{merge(doc(name, fields))}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want string) {
		t.Helper()
		if got := s.SessionPhase("room", 2); got != want {
			t.Fatalf("phase = %q, want %q", got, want)
		}
	}
	check("connected")
	write(refereeRequestPrefix+"first_room", nil)
	check("starting")
	write(refereeProfilePrefix+"first_room/User/alice", nil)
	check("starting")
	write(refereeProfilePrefix+"first_room/User/bob", nil)
	check("ready")
	now = now.Add(24 * time.Hour)
	check("ready")
	write(refereeReportPrefix+"first_room/alice", nil)
	check("results")
	write("docs/__gs/m", map[string]*commonpb.Value{"cp": boolean(true), "ebf": boolean(true)})
	check("rematch_waiting")
	write("docs/__gs/m", map[string]*commonpb.Value{"cp": boolean(false), "ebf": boolean(false)})
	check("rematch_locked")
	write(refereeRequestPrefix+"second_room", nil)
	check("starting")
	write(refereeReportPrefix+"foreign_other-room/alice", nil)
	check("starting")
}
