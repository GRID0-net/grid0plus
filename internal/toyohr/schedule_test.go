package toyohr

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
)

func scheduleServiceFor(t *testing.T, at time.Time, rot Rotation) *scheduleService {
	t.Helper()
	svc, _ := newService(t, at)
	svc.rotation = rot
	return &scheduleService{s: svc}
}

func TestEveryGeneratedVsSlotLandsOnATwoHourBoundary(t *testing.T) {
	// The same rule a Splatfest's phases obey: Splatoon 3 files scheduled
	// things into 7200-second slots, so a window with boundaries elsewhere
	// describes a rotation the game cannot place.
	at := time.Date(2026, 9, 5, 13, 37, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{
		RegularStages: []int32{1, 2, 3, 4},
		BankaraRules:  []int32{1, 2},
		BankaraStages: []int32{5, 6, 7, 8},
	})

	resp, err := sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(resp.GetSchedules()) != scheduleHorizon {
		t.Fatalf("got %d slots, want %d", len(resp.GetSchedules()), scheduleHorizon)
	}

	var previousEnd time.Time
	for i, s := range resp.GetSchedules() {
		start := s.GetStartTime().AsTime()
		end := s.GetEndTime().AsTime()

		if !IsSlotAligned(start) || !IsSlotAligned(end) {
			t.Errorf("slot %d runs %s..%s, which is not on a two-hour boundary", i, start, end)
		}
		if end.Sub(start) != scheduleSlot {
			t.Errorf("slot %d is %s long, want %s", i, end.Sub(start), scheduleSlot)
		}
		// No gaps: a gap is a window in which the lobby has no rotation at all.
		if i > 0 && !start.Equal(previousEnd) {
			t.Errorf("slot %d starts at %s but the previous ended at %s", i, start, previousEnd)
		}
		previousEnd = end

		if s.GetRegularSettings() == nil {
			t.Errorf("slot %d offers no Turf War, so its lobby cannot start a match", i)
		}
		// Anarchy runs two modes at once. A slot offering one rule is a lobby
		// with half its buttons inert.
		if got := len(s.GetBankaraSettings()); got != 2 {
			t.Errorf("slot %d offers %d Anarchy rules, want 2", i, got)
		}
	}
}

func TestTheRotationWrapsRatherThanRunningOut(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	// Two stages, two per slot: every slot has to reuse them.
	sc := scheduleServiceFor(t, at, Rotation{RegularStages: []int32{11, 22}})

	resp, err := sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	for i, s := range resp.GetSchedules() {
		stages := s.GetRegularSettings().GetStages()
		// A slot with no stages is a lobby that cannot start a match, which is
		// why pick wraps instead of returning short.
		if len(stages) != 2 {
			t.Fatalf("slot %d offers %d stages, want 2", i, len(stages))
		}
		got := append([]int32(nil), stages...)
		sort.Slice(got, func(a, b int) bool { return got[a] < got[b] })
		if got[0] != 11 || got[1] != 22 {
			t.Errorf("slot %d offers %v, want the two configured stages", i, stages)
		}
	}
}

func TestTheSameRequestAtTheSameTimeGivesTheSameEtag(t *testing.T) {
	at := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	rot := Rotation{RegularStages: []int32{1, 2, 3, 4}}

	first, err := scheduleServiceFor(t, at, rot).SelectVsSchedules(
		context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	second, err := scheduleServiceFor(t, at, rot).SelectVsSchedules(
		context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatalf("select: %v", err)
	}

	// The etag is what a client's conditional request compares. One that moves
	// for no reason costs a needless refetch.
	if got := first.GetEtag(); len(got) != 24 || !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Errorf("Versus ETag = %q, want the captured quoted 24-character version", got)
	}
	if first.GetEtag() != second.GetEtag() {
		t.Errorf("the same rotation produced two etags: %s and %s",
			first.GetEtag(), second.GetEtag())
	}

	// And a different rotation must produce a different one, or the lobby keeps
	// showing yesterday's stages.
	changed, err := scheduleServiceFor(t, at, Rotation{RegularStages: []int32{9, 8, 7, 6}}).
		SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if changed.GetEtag() == first.GetEtag() {
		t.Error("changing the stages did not change the etag")
	}
}

func TestAnAbsoluteVsSlotKeepsItsStagesWhenTheWindowRolls(t *testing.T) {
	rot := Rotation{
		RegularStages: []int32{1, 2, 3, 4, 5, 6, 7, 8},
		BankaraRules:  []int32{1, 2, 3, 4},
		BankaraStages: []int32{9, 10, 11, 12, 13, 14, 15, 16},
	}
	read := func(at time.Time, wantStart time.Time) *toyohrpb.VsSchedule {
		t.Helper()
		resp, err := scheduleServiceFor(t, at, rot).SelectVsSchedules(
			context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, slot := range resp.GetSchedules() {
			if slot.GetStartTime().AsTime().Equal(wantStart) {
				return slot
			}
		}
		t.Fatalf("response at %s did not include slot beginning %s", at, wantStart)
		return nil
	}

	// At 13:00 the noon slot is position 1; after 14:00 it is position 0.
	// Its selected stages must remain a function of that absolute slot.
	start := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	before := read(time.Date(2026, 9, 24, 13, 0, 0, 0, time.UTC), start)
	after := read(time.Date(2026, 9, 24, 14, 30, 0, 0, time.UTC), start)
	if !proto.Equal(before.GetRegularSettings(), after.GetRegularSettings()) {
		t.Errorf("regular stages changed for the same absolute slot: %v then %v",
			before.GetRegularSettings().GetStages(), after.GetRegularSettings().GetStages())
	}
	if len(before.GetBankaraSettings()) != len(after.GetBankaraSettings()) {
		t.Fatalf("Bankara settings count changed: %d then %d",
			len(before.GetBankaraSettings()), len(after.GetBankaraSettings()))
	}
	for i := range before.GetBankaraSettings() {
		if !proto.Equal(before.GetBankaraSettings()[i], after.GetBankaraSettings()[i]) {
			t.Errorf("Bankara stages changed for the same absolute slot at mode %d", i)
		}
	}
	if !proto.Equal(before.GetXSettings(), after.GetXSettings()) {
		t.Errorf("X stages changed for the same absolute slot")
	}
	if !proto.Equal(before.GetLeagueSettings(), after.GetLeagueSettings()) {
		t.Errorf("League stages changed for the same absolute slot")
	}
}

func TestRankedRulesAreStablePerSlotAndRotateAcrossSlots(t *testing.T) {
	rules := []int32{1, 2, 3, 4}
	first := permuteRules(rules, 206500)
	if got := permuteRules(rules, 206500); !slices.Equal(got, first) {
		t.Fatalf("rule order changed for the same slot: %v then %v", first, got)
	}
	var changed bool
	for slot := int64(206501); slot < 206521; slot++ {
		if got := permuteRules(rules, slot); !slices.Equal(got, first) {
			changed = true
			break
		}
	}
	if !changed {
		t.Fatalf("ranked rule order did not change across twenty slots: %v", first)
	}
	if len(first) != len(rules) {
		t.Fatalf("got %d rules, want %d", len(first), len(rules))
	}
	for _, rule := range rules {
		if !slices.Contains(first, rule) {
			t.Errorf("permutation omitted configured rule %d: %v", rule, first)
		}
	}
}

func TestAWeekOfGeneratedModesDoesNotRepeatAStageWithinASlot(t *testing.T) {
	rot := Rotation{
		RegularStages: []int32{6, 23, 7, 3, 5, 2, 21, 20},
		BankaraRules:  []int32{1, 2, 3, 4},
		BankaraStages: []int32{22, 17, 21, 16, 4, 6, 20, 7},
	}
	base := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 84; i++ {
		slot := (&Service{}).vsSchedule(base.Add(time.Duration(i)*scheduleSlot), rot, "default", "test")
		seen := map[int32]string{}
		check := func(mode string, stages []int32) {
			t.Helper()
			for _, stage := range stages {
				if prior, ok := seen[stage]; ok {
					t.Errorf("slot %d repeats stage %d in %s and %s", i, stage, prior, mode)
				}
				seen[stage] = mode
			}
		}
		check("regular", slot.GetRegularSettings().GetStages())
		for j, setting := range slot.GetBankaraSettings() {
			check(fmt.Sprintf("bankara-%d", j), setting.GetStages())
		}
		check("X", slot.GetXSettings().GetStages())
		check("league", slot.GetLeagueSettings().GetStages())
	}
}

func TestAClientAskingForALimitedWindowGetsOne(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{RegularStages: []int32{1, 2}})

	resp, err := sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{
		SelectDuration: durationpb.New(6 * time.Hour),
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got := len(resp.GetSchedules()); got != 3 {
		t.Errorf("six hours gave %d slots, want 3", got)
	}

	// But not more than the horizon: a schedule stretching further than the
	// operator has thought about is a promise about stages that may change.
	resp, err = sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{
		SelectDuration: durationpb.New(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got := len(resp.GetSchedules()); got != scheduleHorizon {
		t.Errorf("a month gave %d slots, want the horizon's %d", got, scheduleHorizon)
	}
}

func TestNoConfiguredRotationIsAnEmptyScheduleRatherThanAnError(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{})

	// An empty schedule is what the game sees between seasons on a real server,
	// so it is a state it has a reading for. An error is not.
	resp, err := sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatalf("select with no rotation configured: %v", err)
	}
	if len(resp.GetSchedules()) != 0 {
		t.Errorf("got %d slots from an empty rotation", len(resp.GetSchedules()))
	}

	// Unlike Vs, Coop content never depended on Rotation -- coop_ids.go's
	// tables are real ids regardless of operator config, and
	// liveNormalCoopShifts (coop_live.go) generates the Normal rotation live
	// off the clock alone. So an empty Rotation still yields a real,
	// live-generated Salmon Run schedule, not an empty one -- a real server
	// always answers Coop alongside Vs/League (schedule_preview.go's own
	// liveCoopFillEnabled comment).
	coop, err := sc.SelectCoopSchedules(context.Background(), &toyohrpb.SelectCoopSchedulesRequest{})
	if err != nil {
		t.Fatalf("select coop: %v", err)
	}
	if got, want := len(coop.GetSchedules()), coopLiveHorizonShifts; got != want {
		t.Errorf("got %d co-op shifts with no rotation configured, want %d live-generated Normal shifts", got, want)
	}
}

// TestLeagueAndVsParamsAnswerRatherThanUnimplemented is the regression test
// for a real hardware finding (2026-09-23): neither RPC had a handler at
// all, so the console got Unimplemented instead of the empty answer
// documented as this project's policy for un-fabricatable game content --
// right in the middle of the hall's own startup call sequence, immediately
// before the game crashed at the Splatfest region prompt.
func TestLeagueAndVsParamsAnswerRatherThanUnimplemented(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{})

	league, err := sc.SelectLeagueSchedules(context.Background(), &toyohrpb.SelectLeagueSchedulesRequest{})
	if err != nil {
		t.Fatalf("select league schedules: %v", err)
	}
	if len(league.GetSchedules()) != 0 {
		t.Errorf("got %d league schedules, want 0 (no event to describe)", len(league.GetSchedules()))
	}

	params, err := sc.SelectVsParams(context.Background(), &toyohrpb.SelectVsParamsRequest{Etag: "abc"})
	if err != nil {
		t.Fatalf("select vs params: %v", err)
	}
	// TestSelectVsParamsNeverAnswersEmpty below is the regression test for
	// why this must be non-nil; kept minimal here since this test's own
	// point is "answers at all", not the shape.
	if params.GetParams() == nil {
		t.Error("params is nil -- this is what stalls a real console before it ever calls SubscribePresences")
	}
}

// TestSelectVsParamsNeverAnswersEmptyOrStale is the regression test for a
// real crash: NextendoNetwork/splatoon-3 measured (facts only, not its code)
// that a real server never sends VsParams with nil `params`, and separately
// that its two timestamps must ride the current rotation rather than
// whatever fixed values a capture happened to record -- stale ones leave the
// client unable to tie the current rotation to its parameters. Reproduced on
// SwitchNet's own hardware on 2026-09-24: the console called this RPC and
// then went silent before ever calling PresenceService/SubscribePresences.
func TestSelectVsParamsNeverAnswersEmptyOrStale(t *testing.T) {
	at := time.Date(2026, 9, 5, 13, 37, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{})

	resp, err := sc.SelectVsParams(context.Background(), &toyohrpb.SelectVsParamsRequest{})
	if err != nil {
		t.Fatalf("select vs params: %v", err)
	}
	params := resp.GetParams()
	if params == nil {
		t.Fatal("params is nil -- this stalls a real console")
	}
	if params.GetName() == "" {
		t.Error("name is empty")
	}
	for name, ts := range map[string]time.Time{
		"timestamp1": params.GetTimestamp1().AsTime(),
		"timestamp2": params.GetTimestamp2().AsTime(),
	} {
		if ts.IsZero() || ts.Year() < at.Year() {
			t.Errorf("%s = %v, want it riding the current clock rather than a stale fixed value", name, ts)
		}
	}
	if !params.GetTimestamp1().AsTime().Before(at) || !params.GetTimestamp2().AsTime().After(at) {
		t.Errorf("VsParams window [%v, %v] does not contain the client's current time %v",
			params.GetTimestamp1().AsTime(), params.GetTimestamp2().AsTime(), at)
	}
	if params.GetAttributes() == nil {
		t.Error("attributes is nil, want an empty-but-present MapValue")
	}
	if len(resp.GetEtag()) != 24 || !strings.HasPrefix(resp.GetEtag(), `"`) || !strings.HasSuffix(resp.GetEtag(), `"`) {
		t.Errorf("etag = %q, want a server-generated quoted 24-character version", resp.GetEtag())
	}
	repeat, err := sc.SelectVsParams(context.Background(), &toyohrpb.SelectVsParamsRequest{Etag: "client-version"})
	if err != nil {
		t.Fatalf("repeat select vs params: %v", err)
	}
	if repeat.GetEtag() != resp.GetEtag() {
		t.Errorf("etag changed with request ETag: %q -> %q", resp.GetEtag(), repeat.GetEtag())
	}
	if !proto.Equal(repeat.GetParams().GetTimestamp1(), params.GetTimestamp1()) ||
		!proto.Equal(repeat.GetParams().GetTimestamp2(), params.GetTimestamp2()) {
		t.Error("repeated VsParams read changed its validity window within a slot")
	}
}

func TestVsParamsRevisionIsIndependentOfTheRotation(t *testing.T) {
	rotation := Rotation{RegularStages: []int32{1, 2}}
	first := scheduleServiceFor(t, time.Date(2026, 9, 5, 13, 37, 0, 0, time.UTC), rotation)
	second := scheduleServiceFor(t, time.Date(2026, 9, 5, 15, 37, 0, 0, time.UTC), rotation)
	request := &toyohrpb.SelectVsParamsRequest{Target: "tenants/current/targets/default"}
	a, err := first.SelectVsParams(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.SelectVsParams(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if a.GetParams().GetParamsSetId() == scheduleSetID(AlignToSlot(first.s.now())) {
		t.Fatal("params revision unexpectedly equals the first rotation id")
	}
	if a.GetParams().GetParamsSetId() != b.GetParams().GetParamsSetId() || a.GetEtag() != b.GetEtag() {
		t.Fatal("unchanged tuning data acquired a new revision at the rotation boundary")
	}
	if !a.GetParams().GetTimestamp1().AsTime().Before(b.GetParams().GetTimestamp1().AsTime()) {
		t.Fatal("params validity window did not advance with the rotation")
	}
}

// TestSelectVsParamsNameCarriesTheRequestsOwnTarget is the regression test
// for a second and third real crash on the same RPC: a 2026-09-24 decompile
// of Splatoon 3 v11.3.0's own main.nso found the client's resource-name
// parser for a VsParams requires the literal shape
// "tenants/<t>/targets/<target>/vsParams/<rest>". The first fix answered
// "tenants/<t>/vsParams/<id>", missing that segment. The second fix echoed
// the request's Target field back whole -- but a VM-side loopback capture
// caught its real value, "tenants/current/targets/default", a full
// resource path using the same "current" alias convention this package
// resolves elsewhere, not a bare id; echoing it whole built a name with two
// nested "targets" segments and reproduced the identical crash address a
// third time. Only the target's own last segment belongs in the response.
func TestSelectVsParamsNameCarriesTheRequestsOwnTarget(t *testing.T) {
	at := time.Date(2026, 9, 5, 13, 37, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{})

	resp, err := sc.SelectVsParams(context.Background(),
		&toyohrpb.SelectVsParamsRequest{Target: "tenants/current/targets/default"})
	if err != nil {
		t.Fatalf("select vs params: %v", err)
	}
	name := resp.GetParams().GetName()
	segs := strings.Split(strings.TrimPrefix(name, sc.s.tenantPath()+"/"), "/")
	if len(segs) < 4 || segs[0] != "targets" || segs[1] != "default" || segs[2] != "vsParams" {
		t.Errorf("name segments = %v, want [targets default vsParams <id>] -- this is what crashes a real console", segs)
	}
}

// TestSelectVsParamsAttributesMatchBothWorkingCaptures is the regression
// test for the ten VsParams.Attributes keys both preserved working
// Nextendo hall captures agree on byte-for-byte (see vsParamsAttributes'
// own doc comment) -- SwitchNet used to send an empty Attributes map.
func TestSelectVsParamsAttributesMatchBothWorkingCaptures(t *testing.T) {
	at := time.Date(2026, 9, 5, 13, 37, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{})

	resp, err := sc.SelectVsParams(context.Background(), &toyohrpb.SelectVsParamsRequest{})
	if err != nil {
		t.Fatalf("select vs params: %v", err)
	}
	fields := resp.GetParams().GetAttributes().GetFields()

	wantInt := map[string]int64{
		"DivideXRate": 2000, "DivideXRateAtlantic": 500, "DivideXRatePacific": 2000,
		"P2PKeepAliveSec": 8, "PlazaFixedTimestamp": 1726452000,
	}
	for k, want := range wantInt {
		if got := fields[k].GetIntegerValue(); got != want {
			t.Errorf("attributes[%q] = %d, want %d", k, got, want)
		}
	}
	wantBool := map[string]bool{
		"IsAbleToSendImageWithoutSns": true, "IsClampDownActive": true,
		"IsMemHackKickActive": true, "IsOnlinePenaltyActive": true,
	}
	for k, want := range wantBool {
		if got := fields[k].GetBooleanValue(); got != want {
			t.Errorf("attributes[%q] = %v, want %v", k, got, want)
		}
	}

	xrr := fields["XRankingRegionRestrict"].GetMapValue().GetFields()
	if atl := xrr["Atlantic"].GetArrayValue().GetValues(); len(atl) != 1 || atl[0].GetIntegerValue() != 1 {
		t.Errorf("XRankingRegionRestrict.Atlantic = %v, want [1]", atl)
	}
	if pac := xrr["Pacific"].GetArrayValue().GetValues(); len(pac) != 0 {
		t.Errorf("XRankingRegionRestrict.Pacific = %v, want empty", pac)
	}

	// Both preserved successful responses advertise the same first eleven
	// CoopUsedRewardGears pairs in VsParams.
	gears := fields["CoopUsedRewardGears"].GetArrayValue().GetValues()
	if len(gears) != vsParamsRewardGearCount {
		t.Errorf("CoopUsedRewardGears has %d entries, want %d", len(gears), vsParamsRewardGearCount)
	}
	if len(gears) > 0 {
		g := gears[0].GetMapValue().GetFields()
		if g["GearID"].GetIntegerValue() != int64(coopUsedRewardGears[0].ID) || g["GearKind"].GetStringValue() != coopUsedRewardGears[0].Kind {
			t.Errorf("CoopUsedRewardGears[0] = %v, want GearID=%d GearKind=%q", g, coopUsedRewardGears[0].ID, coopUsedRewardGears[0].Kind)
		}
	}
}

func TestSeasonScheduleCarriesTheCapturedLeaderboardFixTime(t *testing.T) {
	at := time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{})
	resp, err := sc.SelectSeasonSchedules(context.Background(),
		&toyohrpb.SelectSeasonSchedulesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetSchedules()) != 1 {
		t.Fatalf("got %d season schedules, want 1", len(resp.GetSchedules()))
	}
	wire, err := proto.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var decoded toyohrpb.SelectSeasonSchedulesResponse
	if err := proto.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	attrs := decoded.GetSchedules()[0].GetAttributes().GetFields()
	got := attrs["LastSeasonLeaderboardFixTime"].GetIntegerValue()
	if got != lastSeasonLeaderboardFixTime {
		t.Errorf("LastSeasonLeaderboardFixTime = %d, want captured value 1780288878", got)
	}
}

// TestSeasonScheduleCarriesTheTargetField is the regression test for a real
// gap: both preserved working hall captures carry an eighth field on every
// SeasonSchedule entry -- decoding to "default" -- entirely absent from
// upstream's own reconstructed proto (which stops at field 7) as well as
// from what SwitchNet was sending. See README.md's own section on this.
func TestSeasonScheduleCarriesTheTargetField(t *testing.T) {
	at := time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{})

	resp, err := sc.SelectSeasonSchedules(context.Background(),
		&toyohrpb.SelectSeasonSchedulesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.GetSchedules()[0].GetTarget(); got != "default" {
		t.Errorf("target with no request target = %q, want %q", got, "default")
	}

	resp, err = sc.SelectSeasonSchedules(context.Background(),
		&toyohrpb.SelectSeasonSchedulesRequest{Target: "tenants/current/targets/event"})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.GetSchedules()[0].GetTarget(); got != "event" {
		t.Errorf("target with a requested target = %q, want %q", got, "event")
	}
}

func TestTheSeasonIsAQuarterAndIsAligned(t *testing.T) {
	// Mid-quarter, so the answer cannot come from the clock by accident.
	at := time.Date(2026, 8, 28, 21, 22, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{})

	resp, err := sc.SelectSeasonSchedules(context.Background(),
		&toyohrpb.SelectSeasonSchedulesRequest{SeasonScheduleCount: 2})
	if err != nil {
		t.Fatalf("select season: %v", err)
	}
	if len(resp.GetSchedules()) != 2 {
		t.Fatalf("got %d seasons, want 2", len(resp.GetSchedules()))
	}
	if got := resp.GetEtag(); len(got) != 24 || !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Errorf("Season ETag = %q, want the captured quoted 24-character version", got)
	}

	first := resp.GetSchedules()[0]
	start := first.GetStartTime().AsTime()
	if start.Month() != time.July || start.Day() != 1 {
		t.Errorf("season starts %s, want 1 July — the quarter containing 28 August", start)
	}
	if !IsSlotAligned(start) {
		t.Errorf("season start %s is not slot-aligned", start)
	}
	// Back to back, so a record made on a boundary has somewhere to go.
	if !resp.GetSchedules()[1].GetStartTime().AsTime().Equal(first.GetEndTime().AsTime()) {
		t.Error("there is a gap between one season and the next")
	}
}

func TestAnUnauthenticatedCallCannotReadTheSchedule(t *testing.T) {
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{RegularStages: []int32{1, 2}})
	as(t, "")

	if _, err := sc.SelectVsSchedules(context.Background(),
		&toyohrpb.SelectVsSchedulesRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("reading the schedule with no caller gave %v, want Unauthenticated", err)
	}
}

func TestARotationWithATypoIsRefusedRatherThanReadAsStageZero(t *testing.T) {
	// Stage 0 is a perfectly plausible valid id, so a zero produced by a failed
	// parse would be indistinguishable from an intended one.
	if _, err := RotationFromStrings([]string{"1", "two"}, nil, nil, 0); err == nil {
		t.Error("a non-numeric stage id was accepted")
	}
	rot, err := RotationFromStrings([]string{"1", " 2 ", ""}, []string{"3"}, []string{"4"}, 1)
	if err != nil {
		t.Fatalf("a valid rotation was refused: %v", err)
	}
	if len(rot.RegularStages) != 2 || rot.RegularStages[1] != 2 {
		t.Errorf("regular stages = %v, want [1 2] with the blank dropped", rot.RegularStages)
	}
	if rot.StagesPerSlot != 1 {
		t.Errorf("stages per slot = %d, want 1", rot.StagesPerSlot)
	}
}

// A real console requests the target as a full alias path. Its returned
// resource names must have the captured target segment, or the game's path
// parser aborts on the NPLN worker thread before the hall finishes booting.
func TestScheduleNamesOnTheWireUseTheRequestedTarget(t *testing.T) {
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	svc, _ := newService(t, at)
	svc.rotation = Rotation{RegularStages: []int32{1, 2}}
	sc := &scheduleService{s: svc}
	const target = "tenants/current/targets/default"
	prefix := svc.tenantPath() + "/targets/default/"

	vs, err := sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := proto.Marshal(vs)
	if err != nil {
		t.Fatal(err)
	}
	var receivedVs toyohrpb.SelectVsSchedulesResponse
	if err := proto.Unmarshal(encoded, &receivedVs); err != nil {
		t.Fatal(err)
	}
	if len(receivedVs.GetSchedules()) == 0 {
		t.Fatal("no versus schedules on the wire")
	}
	for _, entry := range receivedVs.GetSchedules() {
		if !strings.HasPrefix(entry.GetName(), prefix+"vsSchedules/") {
			t.Fatalf("versus name %q does not match the captured path shape", entry.GetName())
		}
	}

	shift := validTeamContestShift()
	shift.Start = at
	shift.End = at.Add(8 * time.Hour)
	if err := svc.SaveCoopShift(context.Background(), shift); err != nil {
		t.Fatal(err)
	}
	coop, err := sc.SelectCoopSchedules(context.Background(), &toyohrpb.SelectCoopSchedulesRequest{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = proto.Marshal(coop)
	if err != nil {
		t.Fatal(err)
	}
	var receivedCoop toyohrpb.SelectCoopSchedulesResponse
	if err := proto.Unmarshal(encoded, &receivedCoop); err != nil {
		t.Fatal(err)
	}
	if len(receivedCoop.GetSchedules()) == 0 {
		t.Fatal("no Salmon Run schedules on the wire")
	}
	for _, entry := range receivedCoop.GetSchedules() {
		if !strings.HasPrefix(entry.GetName(), prefix+"coopSchedules/") {
			t.Fatalf("Salmon Run name %q does not match the captured path shape", entry.GetName())
		}
	}
}

// A working hall response names the previous slot as well as the live one,
// keeps one schedule-set id across the window, and divides four ranked rules
// between two Anarchy variants, X, and League.
func TestVersusWindowUsesOneSetAndFourDistinctRankedModes(t *testing.T) {
	at := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	sc := scheduleServiceFor(t, at, Rotation{
		RegularStages: []int32{1, 2, 3, 4},
		BankaraRules:  []int32{1, 2, 3, 4},
		BankaraStages: []int32{5, 6, 7, 8, 9, 10, 11, 12},
	})
	resp, err := sc.SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetSchedules()) != 12 {
		t.Fatalf("got %d slots, want 12", len(resp.GetSchedules()))
	}
	if got := resp.GetSchedules()[0].GetStartTime().AsTime(); !got.Equal(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("first slot starts %s; want the slot before the current one", got)
	}
	setID := resp.GetSchedules()[0].GetScheduleSetId()
	if _, err := uuid.Parse(setID); err != nil {
		t.Errorf("Versus schedule-set id %q is not a UUID like both working captures: %v", setID, err)
	}
	names := map[string]bool{}
	for i, slot := range resp.GetSchedules() {
		if slot.GetScheduleSetId() != setID || setID == "" {
			t.Errorf("slot %d set id = %q, want shared %q", i, slot.GetScheduleSetId(), setID)
		}
		if names[slot.GetName()] {
			t.Errorf("slot %d repeats resource name %q", i, slot.GetName())
		}
		names[slot.GetName()] = true
		if len(slot.GetBankaraSettings()) != 2 || slot.GetXSettings() == nil || slot.GetLeagueSettings() == nil {
			t.Errorf("slot %d ranked modes: Bankara=%d X=%v League=%v", i,
				len(slot.GetBankaraSettings()), slot.GetXSettings() != nil, slot.GetLeagueSettings() != nil)
		}
	}
}

func TestRotationEtagChangesWithXAndLeagueSettings(t *testing.T) {
	at := time.Date(2026, 9, 5, 13, 0, 0, 0, time.UTC)
	base := Rotation{RegularStages: []int32{1, 2}, BankaraStages: []int32{3, 4, 5, 6}, BankaraRules: []int32{1, 2, 3, 4}}
	read := func(rot Rotation) string {
		t.Helper()
		resp, err := scheduleServiceFor(t, at, rot).SelectVsSchedules(context.Background(), &toyohrpb.SelectVsSchedulesRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return resp.GetEtag()
	}
	initial := read(base)
	base.BankaraRules[2] = 5
	if read(base) == initial {
		t.Error("changing X settings left the schedule ETag unchanged")
	}
	base.BankaraRules[2] = 3
	base.BankaraRules[3] = 5
	if read(base) == initial {
		t.Error("changing League settings left the schedule ETag unchanged")
	}
}
