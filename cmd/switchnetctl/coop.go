package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/toyohr"
)

// coopSchedule implements `switchnetctl coop schedule`.
//
// One shift at a time, the same "one thing typed once, validated, stored"
// shape festBuild gives Splatfests -- except there is no second, BCAT-side
// half of the problem here: every value a coop shift needs is a real
// Nintendo id (internal/toyohr/coop_ids.go), sent directly on the wire, so
// scheduling one shift IS the whole job.
func coopSchedule(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("coop schedule", flag.ExitOnError)
	kindStr := fs.String("kind", "normal", `"normal" or "big_run"`)
	stage := fs.Int("stage", 0, "real Salmon Run stage id -- see 'switchnetctl coop stages'")
	boss := fs.String("boss", "", `king salmonid codename for this shift's Xtrawave, or "" for none -- see 'switchnetctl coop bosses'`)
	weaponsStr := fs.String("weapons", "", "exactly four comma-separated real weapon ids -- see 'switchnetctl coop weapons'")
	kuma := fs.Int64("kuma", 0, "real Grizzco weapon id this shift's boss wave uses -- see 'switchnetctl coop weapons -kuma'")
	rewardGear := fs.Int("reward-gear", 0, "gear reward id for this shift, or 0 for none (not validated -- see internal/toyohr/coop_ids.go)")
	rewardsStr := fs.String("rewards", "", "big_run only: comma-separated scale-tier reward values")
	startStr := fs.String("start", "", "shift start time, RFC3339 -- rounded down to the nearest two-hour slot")
	endStr := fs.String("end", "", "shift end time, RFC3339 -- rounded down to the nearest two-hour slot")
	fs.Usage = func() { fmt.Fprint(os.Stderr, coopScheduleUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *stage == 0 || *weaponsStr == "" || *kuma == 0 || *startStr == "" || *endStr == "" {
		fs.Usage()
		os.Exit(2)
	}

	kind, err := toyohr.ParseCoopShiftKind(*kindStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(2)
	}
	weapons, err := parseWeaponIDs(*weaponsStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: -weapons: %v\n", err)
		os.Exit(2)
	}
	rewards, err := parseInt64List(*rewardsStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: -rewards: %v\n", err)
		os.Exit(2)
	}
	start, err := time.Parse(time.RFC3339, *startStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: -start %q: %v (want RFC3339)\n", *startStr, err)
		os.Exit(2)
	}
	end, err := time.Parse(time.RFC3339, *endStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: -end %q: %v (want RFC3339)\n", *endStr, err)
		os.Exit(2)
	}

	shift := toyohr.CoopShift{
		Kind: kind, StageID: int32(*stage), Boss: *boss,
		KumaWeaponID: *kuma, RewardGearID: int32(*rewardGear), Rewards: rewards,
		Start: toyohr.AlignToSlot(start), End: toyohr.AlignToSlot(end),
	}
	copy(shift.Weapons[:], weapons)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: open database: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	svc := toyohr.NewService(store, slog.Default(), cfg.NPLN.Tenant, time.Now)
	if err := svc.SaveCoopShift(ctx, shift); err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("scheduled coop shift %s\n", shift.ID())
	fmt.Printf("  kind:   %s\n", shift.Kind)
	fmt.Printf("  stage:  %d\n", shift.StageID)
	fmt.Printf("  start:  %s\n", shift.Start.Format(time.RFC3339))
	fmt.Printf("  end:    %s\n", shift.End.Format(time.RFC3339))
}

// coopGenerate implements `switchnetctl coop generate`.
//
// Fills a stretch of the normal rotation automatically rather than typing
// every shift by hand -- see toyohr.GenerateCoopShifts's doc comment for
// what the generator does and, just as importantly, what it is not: a
// discovered Nintendo algorithm, only SwitchNet's own way of picking real
// ids nobody has hand-picked yet. Never touches a slot that already has a
// shift, hand-scheduled or previously generated, so running `coop schedule`
// on any slot afterward always wins.
func coopGenerate(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("coop generate", flag.ExitOnError)
	fromStr := fs.String("from", "", "start of the range to fill, RFC3339 -- rounded down to the nearest two-hour slot")
	count := fs.Int("count", 0, "how many shifts to generate")
	duration := fs.Duration("duration", toyohr.DefaultCoopShiftDuration,
		"length of each shift -- defaults to 40h, Salmon Run's real rotation length")
	seed := fs.Int64("seed", 0, "random seed; 0 picks one from the current time, so two runs differ unless you fix this")
	fs.Usage = func() { fmt.Fprint(os.Stderr, coopGenerateUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *fromStr == "" || *count <= 0 {
		fs.Usage()
		os.Exit(2)
	}

	from, err := time.Parse(time.RFC3339, *fromStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: -from %q: %v (want RFC3339)\n", *fromStr, err)
		os.Exit(2)
	}
	s := *seed
	if s == 0 {
		s = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewPCG(uint64(s), uint64(s)))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: open database: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	svc := toyohr.NewService(store, slog.Default(), cfg.NPLN.Tenant, time.Now)
	created, skipped, err := svc.GenerateAndSaveCoopShifts(ctx, from, *count, *duration, rng)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("generated %d shift(s), %d slot(s) already had one and were left alone\n", len(created), len(skipped))
	for _, s := range created {
		stage, _ := toyohr.CoopStageByID(s.StageID)
		fmt.Printf("  %s  %-24s  %s -> %s\n", s.ID(), stage.Name, s.Start.Format(time.RFC3339), s.End.Format(time.RFC3339))
	}
}

// coopList implements `switchnetctl coop list`.
func coopList(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("coop list", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "switchnetctl coop list\n\nLists every scheduled coop shift, soonest first.\n")
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: open database: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	svc := toyohr.NewService(store, slog.Default(), cfg.NPLN.Tenant, time.Now)
	shifts, err := svc.ListCoopShiftsForOperator(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}
	if len(shifts) == 0 {
		fmt.Println("no coop shifts scheduled")
		return
	}
	for _, s := range shifts {
		stage, _ := toyohr.CoopStageByID(s.StageID)
		fmt.Printf("%s  %-8s  %-24s  %s -> %s\n",
			s.ID(), s.Kind, stage.Name, s.Start.Format(time.RFC3339), s.End.Format(time.RFC3339))
	}
}

// coopDelete implements `switchnetctl coop delete <id>`.
func coopDelete(cfg config.Config, args []string) {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, "switchnetctl: usage: switchnetctl coop delete <id>\n")
		os.Exit(2)
	}
	id := args[0]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: open database: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	svc := toyohr.NewService(store, slog.Default(), cfg.NPLN.Tenant, time.Now)
	if err := svc.DeleteCoopShift(ctx, id); err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("deleted coop shift %s\n", id)
}

// coopStages implements `switchnetctl coop stages`: the real stage id table,
// so an operator does not have to read coop_ids.go to schedule a shift.
func coopStages() {
	for _, s := range toyohr.CoopStages {
		kind := "normal  "
		if s.BigRun {
			kind = "big_run "
		}
		fmt.Printf("%-4d  %s %s\n", s.ID, kind, s.Name)
	}
}

// coopWeapons implements `switchnetctl coop weapons` (and `-kuma` for the
// Grizzco-only pool).
func coopWeapons(args []string) {
	fs := flag.NewFlagSet("coop weapons", flag.ExitOnError)
	kuma := fs.Bool("kuma", false, "list the Grizzco kuma-weapon pool instead of the main weapon pool")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	list := toyohr.CoopWeapons
	if *kuma {
		list = toyohr.CoopKumaWeapons
	}
	for _, w := range list {
		fmt.Printf("%-6d  %s\n", w.ID, w.Name)
	}
}

// coopBosses implements `switchnetctl coop bosses`.
func coopBosses() {
	for _, b := range toyohr.CoopBosses {
		fmt.Println(b)
	}
}

func parseWeaponIDs(s string) ([]int64, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return nil, fmt.Errorf("got %d weapon id(s), want exactly 4", len(parts))
	}
	out := make([]int64, 0, 4)
	for _, p := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a weapon id: %w", p, err)
		}
		out = append(out, id)
	}
	return out, nil
}

func parseInt64List(s string) ([]int64, error) {
	if s == "" {
		return nil, nil
	}
	var out []int64
	for _, p := range strings.Split(s, ",") {
		v, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer: %w", p, err)
		}
		out = append(out, v)
	}
	return out, nil
}

const coopScheduleUsage = `switchnetctl coop schedule -kind <normal|big_run> -stage <id> -weapons <id,id,id,id> -kuma <id> -start <RFC3339> -end <RFC3339> [-boss <codename>] [-reward-gear <id>] [-rewards <id,id,...>]

Schedules one Salmon Run rotation shift and registers it, so the console's
SelectCoopSchedules RPC starts serving it. Every id must be a real Nintendo
one -- 'switchnetctl coop stages', '... weapons', '... weapons -kuma' and
'... bosses' print the tables to pick from; a made-up id is refused rather
than sent to a console that cannot resolve it against its own assets.

-kind big_run requires a Big Run stage (coop stages marks which); -kind
normal refuses one. Running this again with the same -kind and -start
replaces the shift rather than creating a second one -- the id is derived
from those two values, never typed.

Requires a reachable database -- see 'switchnetctl account create -h'.
`

const coopGenerateUsage = `switchnetctl coop generate -from <RFC3339> -count <N> [-duration 40h] [-seed <N>]

Fills count back-to-back normal-rotation slots starting at -from with real,
randomly-picked stage/weapon/boss ids -- SwitchNet's own generator, not a
recovered Nintendo algorithm (see toyohr.GenerateCoopShifts). Never
overwrites a slot that already has a shift, whether it was hand-scheduled
with 'coop schedule' or produced by an earlier 'coop generate' -- run
'coop schedule' on any specific slot afterward to correct it by hand, and a
later 'coop generate' over the same range will leave that correction alone.

-duration defaults to 40h, Salmon Run's real rotation length (confirmed
against public sources, not this project's own data -- see
toyohr.DefaultCoopShiftDuration's comment). -seed fixes the randomness for a
reproducible run; omit it and every run differs.

Requires a reachable database -- see 'switchnetctl account create -h'.
`
