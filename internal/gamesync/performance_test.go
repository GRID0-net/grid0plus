package gamesync

import (
	"fmt"
	"testing"
	"time"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
)

func BenchmarkStartupVerdict(b *testing.B) {
	s := &Server{docs: newStoreSet(time.Now)}
	profile := refereeProfilePrefix + "battle_room/User/player"
	s.docs.in("room").seed(&syncpb.Document{Name: profile, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"player_info": {ValueType: &commonpb.Value_BytesValue{BytesValue: make([]byte, 32<<10)}},
	}}})
	verdict := &syncpb.Document{Name: refereeResultPrefix + "battle_room", Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"status": intValue(-1)}}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.startupVerdict(verdict, "room", "userSessions/player")
	}
}

func BenchmarkPeerFanout(b *testing.B) {
	for _, players := range []int{2, 4, 8, 32} {
		b.Run(fmt.Sprint(players), func(b *testing.B) {
			w := newWatchers()
			var subs []*subscriber
			for i := 0; i < players; i++ {
				name := fmt.Sprintf("player-%d", i)
				sub := w.subscribe(name, "userSessions/"+name, "room")
				w.addTarget(sub, &syncpb.Target{Name: "targets/9", TargetType: &syncpb.Target_Documents{Documents: &syncpb.DocumentsTarget{Documents: []string{peerInboxPrefix + name}}}})
				subs = append(subs, sub)
			}
			changes := []*syncpb.DocumentChange{{Document: &syncpb.Document{Name: peerInboxPrefix + "player-0"}}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w.publish("room", changes)
				<-subs[0].changes
			}
		})
	}
}

func BenchmarkStoreLookupParallel(b *testing.B) {
	s := newStoreSet(time.Now)
	s.in("room")
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.in("room")
		}
	})
}

func BenchmarkCollectionWithRetainedHistory(b *testing.B) {
	s := newStore(time.Now)
	for i := 0; i < 2048; i++ {
		s.seed(&syncpb.Document{Name: fmt.Sprintf("docs/history/%04d", i)})
	}
	for i := 0; i < 8; i++ {
		s.seed(&syncpb.Document{Name: fmt.Sprintf("docs/__us/%d", i)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.list("docs/__us")
	}
}

func BenchmarkFanoutAcrossIndependentRooms(b *testing.B) {
	w := newWatchers()
	var recipient *subscriber
	for room := 0; room < 128; room++ {
		for player := 0; player < 8; player++ {
			sub := w.subscribe(fmt.Sprint(player), fmt.Sprint(player), fmt.Sprint(room))
			w.addTarget(sub, &syncpb.Target{Name: "targets/9", TargetType: &syncpb.Target_Documents{Documents: &syncpb.DocumentsTarget{Documents: []string{fmt.Sprintf("docs/peer/%d", player)}}}})
			if room == 0 && player == 0 {
				recipient = sub
			}
		}
	}
	changes := []*syncpb.DocumentChange{{Document: &syncpb.Document{Name: "docs/peer/0"}}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.publish("0", changes)
		<-recipient.changes
	}
}
