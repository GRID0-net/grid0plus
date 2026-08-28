package toyohr

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

// scheduleService is nn.npln.toyohr.v1.Schedule: the stage and mode rotation.
type scheduleService struct {
	toyohrpb.UnimplementedScheduleServer
	s *Service
}

// Rotation is the stage and rule rotation an operator configured.
//
// **The mechanics here are SwitchNet's; the numbers are not.** Slot alignment,
// windowing, ordering and the etag are all this package's to decide and are
// implemented below. The stage and rule identifiers are Splatoon 3's own
// internal numbering, which nothing public documents — so they are operator
// input rather than a table in this source. Inventing them would be an invented
// game internal, and an invalid stage number's behaviour on the console is
// itself unknown (see docs/PHASE2_PLAN.md's capture list).
//
// An empty Rotation is legitimate: the schedule handlers then answer with no
// schedules at all, which is what the game sees on a real server between
// seasons, and each unanswered request is logged so the absence is visible.
type Rotation struct {
	// RegularStages are the stage ids Turf War rotates through, in order.
	RegularStages []int32
	// BankaraRules are the rule ids Anarchy rotates through, in order.
	BankaraRules []int32
	// BankaraStages are the stage ids Anarchy rotates through, in order.
	BankaraStages []int32

	// StagesPerSlot is how many stages one slot offers. Two on a real server;
	// configurable because it depends on how many stages the operator listed.
	StagesPerSlot int
}

// scheduleSlot is how long one entry in the rotation lasts.
//
// Two hours, the same bucket a Splatfest's phases must land on. Splatoon 3
// files every scheduled thing into 7200-second slots, so a schedule whose
// boundaries are elsewhere describes windows the game cannot place.
const scheduleSlot = slotSeconds * time.Second

// scheduleHorizon is how far ahead schedules are generated.
//
// Twelve slots — a day. The game asks for a window and shows the next few
// rotations; generating more costs nothing but says less about what is actually
// planned, and generating fewer leaves the lobby's "coming up" list short.
const scheduleHorizon = 12

// SelectVsSchedules returns the versus rotation.
//
// Generated rather than stored: a rotation is a deterministic function of the
// clock and the operator's stage list, so there is nothing to keep in sync. The
// same request at the same time always produces the same answer, which is what
// makes the etag meaningful.
func (sc *scheduleService) SelectVsSchedules(
	ctx context.Context, req *toyohrpb.SelectVsSchedulesRequest,
) (*toyohrpb.SelectVsSchedulesResponse, error) {
	if _, ok := sc.s.caller(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}

	rot := sc.s.rotation
	if len(rot.RegularStages) == 0 && len(rot.BankaraStages) == 0 {
		sc.s.noteNoRotation("vs")
		return &toyohrpb.SelectVsSchedulesResponse{}, nil
	}

	from := AlignToSlot(sc.s.now())
	slots := scheduleHorizon
	if d := req.GetSelectDuration().AsDuration(); d > 0 {
		// The client says how much it wants. Honoured up to the horizon: a
		// client asking for a week would otherwise get one, and a schedule
		// stretching further than the operator has thought about is a promise
		// about stages that may change.
		if asked := int(d / scheduleSlot); asked > 0 && asked < slots {
			slots = asked
		}
	}

	schedules := make([]*toyohrpb.VsSchedule, 0, slots)
	for i := 0; i < slots; i++ {
		start := from.Add(time.Duration(i) * scheduleSlot)
		schedules = append(schedules, sc.s.vsSchedule(start, i, rot))
	}

	return &toyohrpb.SelectVsSchedulesResponse{
		Schedules: schedules,
		Etag:      etagOf(schedules),
	}, nil
}

// vsSchedule renders one two-hour slot.
func (s *Service) vsSchedule(start time.Time, index int, rot Rotation) *toyohrpb.VsSchedule {
	end := start.Add(scheduleSlot)
	setID := scheduleSetID(start)

	out := &toyohrpb.VsSchedule{
		Name:          s.tenantPath() + "/vsSchedules/" + setID,
		StartTime:     timestamppb.New(start),
		EndTime:       timestamppb.New(end),
		ScheduleSetId: setID,
	}
	if stages := pick(rot.RegularStages, index, rot.stagesPerSlot()); len(stages) > 0 {
		out.RegularSettings = &toyohrpb.RegularSettings{Stages: stages}
	}
	if stages := pick(rot.BankaraStages, index, rot.stagesPerSlot()); len(stages) > 0 {
		// One BankaraSettings per rule the operator listed. Anarchy runs two
		// modes at once — Series and Open — and a slot offering one rule is a
		// lobby with half its buttons inert.
		for r, rule := range rot.BankaraRules {
			out.BankaraSettings = append(out.BankaraSettings, &toyohrpb.BankaraSettings{
				Rule:   rule,
				Stages: pick(rot.BankaraStages, index+r, rot.stagesPerSlot()),
			})
		}
	}
	return out
}

// SelectCoopSchedules returns the Salmon Run rotation.
//
// Answered empty. A co-op shift names a stage, a boss, four weapon ids and a
// reward — every one of them a Splatoon 3 internal number, and unlike the versus
// rotation there is nothing useful left once they are removed. An empty
// response is what the game sees when no shift is scheduled, so it is a state
// it has a reading for; a shift with invented ids is not.
//
// TODO(capture-needed): the stage, boss, weapon and reward identifiers, and
// what the console does with an unknown one. Change: this function, plus a
// CoopShifts field on Rotation.
func (sc *scheduleService) SelectCoopSchedules(
	ctx context.Context, _ *toyohrpb.SelectCoopSchedulesRequest,
) (*toyohrpb.SelectCoopSchedulesResponse, error) {
	if _, ok := sc.s.caller(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	sc.s.noteNoRotation("coop")
	return &toyohrpb.SelectCoopSchedulesResponse{}, nil
}

// SelectSeasonSchedules returns the current season.
//
// A season is a window with a name and nothing else this layer has to invent:
// three months, aligned to a slot boundary like everything else. It is
// generated because the game uses it to bucket records, and a client with no
// season has nowhere to file what it did.
func (sc *scheduleService) SelectSeasonSchedules(
	ctx context.Context, req *toyohrpb.SelectSeasonSchedulesRequest,
) (*toyohrpb.SelectSeasonSchedulesResponse, error) {
	if _, ok := sc.s.caller(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "")
	}

	count := int(req.GetSeasonScheduleCount())
	if count <= 0 || count > 4 {
		count = 1
	}

	now := sc.s.now().UTC()
	schedules := make([]*toyohrpb.SeasonSchedule, 0, count)
	start := seasonStart(now)
	for i := 0; i < count; i++ {
		end := start.AddDate(0, 3, 0)
		setID := scheduleSetID(start)
		schedules = append(schedules, &toyohrpb.SeasonSchedule{
			Name:          sc.s.tenantPath() + "/seasonSchedules/" + setID,
			StartTime:     timestamppb.New(start),
			EndTime:       timestamppb.New(end),
			ScheduleSetId: setID,
		})
		start = end
	}

	return &toyohrpb.SelectSeasonSchedulesResponse{
		Schedules: schedules,
		Etag:      seasonEtag(schedules),
	}, nil
}

// seasonStart returns the start of the three-month season containing t.
//
// Quarters from January, at midnight UTC — already slot-aligned, since midnight
// is a multiple of two hours. Arbitrary but stated: a season boundary is
// editorial on a private server, and a rule an operator can predict beats one
// derived from when the server happened to start.
func seasonStart(t time.Time) time.Time {
	t = t.UTC()
	month := time.Month((int(t.Month())-1)/3*3 + 1)
	return time.Date(t.Year(), month, 1, 0, 0, 0, 0, time.UTC)
}

// pick returns n identifiers from a rotation, starting at index and wrapping.
//
// Wrapping rather than running out: a rotation is meant to be cyclic, and a
// slot with no stages is a lobby that cannot start a match.
func pick(from []int32, index, n int) []int32 {
	if len(from) == 0 || n <= 0 {
		return nil
	}
	if n > len(from) {
		n = len(from)
	}
	out := make([]int32, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, from[((index*n)+i)%len(from)])
	}
	return out
}

func (r Rotation) stagesPerSlot() int {
	if r.StagesPerSlot > 0 {
		return r.StagesPerSlot
	}
	// Two, which is what a real rotation offers per slot.
	return 2
}

// scheduleSetID names a slot.
//
// Derived from the slot's start, so the same slot has the same id every time it
// is described — which is what lets a client cache one and compare.
func scheduleSetID(start time.Time) string {
	return fmt.Sprintf("%d", start.UTC().Unix()/slotSeconds)
}

// etagOf digests a generated schedule list.
//
// Over the values the client would act on, so it changes when and only when the
// answer does. The point is the client's conditional request: an etag that moves
// for no reason costs a needless refetch, and one that fails to move leaves the
// lobby showing yesterday's rotation.
func etagOf(schedules []*toyohrpb.VsSchedule) string {
	h := sha1.New()
	for _, s := range schedules {
		fmt.Fprintf(h, "%s|%d|%d|", s.ScheduleSetId,
			s.StartTime.GetSeconds(), s.EndTime.GetSeconds())
		writeStages(h, s.GetRegularSettings().GetStages())
		for _, b := range s.GetBankaraSettings() {
			fmt.Fprintf(h, "r%d:", b.GetRule())
			writeStages(h, b.GetStages())
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func seasonEtag(schedules []*toyohrpb.SeasonSchedule) string {
	h := sha1.New()
	for _, s := range schedules {
		fmt.Fprintf(h, "%s|%d|%d|", s.ScheduleSetId,
			s.StartTime.GetSeconds(), s.EndTime.GetSeconds())
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeStages(h interface{ Write([]byte) (int, error) }, stages []int32) {
	for _, st := range stages {
		fmt.Fprintf(h, "%d,", st)
	}
	fmt.Fprint(h, ";")
}

// noteNoRotation logs, once per kind, that a schedule was asked for and there
// is nothing configured to answer with.
//
// Once, because the console asks on a timer. Logged at all because an empty
// schedule is indistinguishable from a broken one from the console's side, and
// this is the line that tells the operator which it is.
func (s *Service) noteNoRotation(kind string) {
	if _, seen := s.noRotation.LoadOrStore(kind, struct{}{}); seen {
		return
	}
	s.log.Info("toyohr: a schedule was requested and no rotation is configured",
		"kind", kind, "hint", "set npln.rotation in config")
}

// RotationFromStrings parses the operator's configured rotation.
//
// Identifiers are given as strings in YAML so a typo is reported here, with the
// offending value, rather than silently becoming zero — and stage 0 is a
// perfectly plausible valid id, so a zero from a parse failure would be
// indistinguishable from an intended one.
func RotationFromStrings(regular, bankaraRules, bankaraStages []string, stagesPerSlot int) (Rotation, error) {
	var out Rotation
	var err error
	if out.RegularStages, err = parseIDs("regular_stages", regular); err != nil {
		return Rotation{}, err
	}
	if out.BankaraRules, err = parseIDs("bankara_rules", bankaraRules); err != nil {
		return Rotation{}, err
	}
	if out.BankaraStages, err = parseIDs("bankara_stages", bankaraStages); err != nil {
		return Rotation{}, err
	}
	out.StagesPerSlot = stagesPerSlot
	return out, nil
}

func parseIDs(field string, values []string) ([]int32, error) {
	out := make([]int32, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		var n int32
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return nil, fmt.Errorf("npln.rotation.%s: %q is not a number", field, v)
		}
		out = append(out, n)
	}
	return out, nil
}
