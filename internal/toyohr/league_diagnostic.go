package toyohr

import (
	"os"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// leagueDiagnosticPath is a temporary, disposable experiment: if set, replay
// the raw captured SelectLeagueSchedules bytes from this path, with every
// timestamp shifted so the earliest one lands on the current 2-hour slot --
// the same technique NextendoNetwork/splatoon-3's own serveShifted uses for
// all its captured schedule types. Not wired into any committed config; test
// on the VM directly and remove this file plus its one call site in
// SelectLeagueSchedules if it does not help.
var leagueDiagnosticPath = os.Getenv("SWITCHNET_NPLN_DIAGNOSTIC_LEAGUE_PATH")

func leagueDiagnosticResponse(now time.Time) (*toyohrpb.SelectLeagueSchedulesResponse, bool) {
	if leagueDiagnosticPath == "" {
		return nil, false
	}
	raw, err := os.ReadFile(leagueDiagnosticPath)
	if err != nil {
		return nil, false
	}
	resp := &toyohrpb.SelectLeagueSchedulesResponse{}
	if proto.Unmarshal(raw, resp) != nil {
		return nil, false
	}

	var earliest time.Time
	walkLeagueTimestamps(resp, func(ts *timestamppb.Timestamp) {
		if ts.GetSeconds() <= 0 {
			return
		}
		t := ts.AsTime()
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	})
	if earliest.IsZero() {
		return resp, true
	}
	delta := AlignToSlot(now).Sub(AlignToSlot(earliest))
	if delta != 0 {
		walkLeagueTimestamps(resp, func(ts *timestamppb.Timestamp) {
			if ts.GetSeconds() <= 0 {
				return // never shift an unset/sentinel timestamp
			}
			shifted := timestamppb.New(ts.AsTime().Add(delta))
			ts.Seconds = shifted.Seconds
			ts.Nanos = shifted.Nanos
		})
	}
	return resp, true
}

// walkLeagueTimestamps visits every google.protobuf.Timestamp field anywhere
// in msg, recursively -- the same generic walk NextendoNetwork/splatoon-3's
// walkTimestamps does, reimplemented here rather than imported since that is
// a private, uncommitted binary this project has no dependency on.
func walkLeagueTimestamps(msg proto.Message, visit func(*timestamppb.Timestamp)) {
	if ts, ok := msg.(*timestamppb.Timestamp); ok {
		visit(ts)
		return
	}
	msg.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList() && fd.Kind() == protoreflect.MessageKind:
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				walkLeagueTimestamps(list.Get(i).Message().Interface(), visit)
			}
		case fd.Kind() == protoreflect.MessageKind && !fd.IsMap():
			walkLeagueTimestamps(v.Message().Interface(), visit)
		}
		return true
	})
}
