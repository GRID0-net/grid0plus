package rendezvous

import (
	"github.com/n-popescu/switchnet/internal/nex"
)

// Notifications (protocol 14): the server calling the console. The console
// never answers these (Kinnay, RMC Protocol, "Remarks").
// https://github.com/kinnay/NintendoClients/wiki/Notification-Protocol
const ProtocolNotifications = 0x0E

const methodProcessNotificationEvent = 1

// Notification types this server sends.
const (
	NotifyParticipate          = 3001   // a player joined; p1 gid, p2 pid, str message, p3 count
	NotifyParticipantLeft      = 3007   // a player disconnected; p1 gid, p2 pid
	NotifyParticipationEnded   = 3008   // a player left on purpose; p1 gid, p2 pid
	NotifyOwnershipChanged     = 4000   // p1 gid, p2 new owner
	NotifyGatheringUnregisterd = 109000 // p1 gid
	NotifyHostChanged          = 110000 // p1 gid
	NotifyAddedToGathering     = 122000 // p1 gid, p2 pid: joined by someone else
)

// NotificationEvent is the Switch NotificationEvent, revision 0: PID source,
// u32 type, three u64 parameters and a string -- and no map. The revision-1
// map is not sent: NextendoNetwork records the Switch rejecting the
// map-carrying and u32 variants outright (facts only).
type NotificationEvent struct {
	Source   uint64
	Type     uint32
	Param1   uint64
	Param2   uint64
	StrParam string
	Param3   uint64
}

// WriteNotificationEvent writes one event.
func WriteNotificationEvent(w *nex.Writer, e NotificationEvent) {
	w.Struct(0, func(w *nex.Writer) {
		w.PID(e.Source)
		w.U32(e.Type)
		w.U64(e.Param1)
		w.U64(e.Param2)
		w.String(e.StrParam)
		w.U64(e.Param3)
	})
}

// ReadNotificationEvent reads one event (tests play the console with it).
func ReadNotificationEvent(r *nex.Reader) NotificationEvent {
	var e NotificationEvent
	r.Struct(func(r *nex.Reader, _ uint8) {
		e.Source = r.PID()
		e.Type = r.U32()
		e.Param1 = r.U64()
		e.Param2 = r.U64()
		e.StrParam = r.String()
		e.Param3 = r.U64()
	})
	return e
}

// Notify sends an event to each player that is connected. Sent on the
// target's own connection, so a player's notifications arrive in the order
// they were sent, and -- when the target is the caller of the method that
// caused it -- before that method's response, which is the order Pia expects.
func (s *Server) Notify(e NotificationEvent, pids ...uint64) {
	w := nex.NewWriter()
	WriteNotificationEvent(w, e)
	body := w.Bytes()
	for _, pid := range pids {
		if c := s.Player(pid); c != nil {
			if err := c.Call(ProtocolNotifications, methodProcessNotificationEvent, body); err != nil {
				s.log.Debug("nex: notification not delivered", "pid", pid, "type", e.Type, "error", err)
			}
		}
	}
}
