package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/bcat"
	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/toyohr"
)

// festBuild implements `switchnetctl fest build`.
//
// # Why this is one command
//
// A Splatfest is four values that have to agree exactly:
//
//	the fest id            announced in the NPLN schedule
//	the resource name      announced, AND the string the pack's decryption IV
//	                       is derived from
//	the revision           announced, AND carried in the pack's own summary.yml
//	the pack keys          returned by GetFestDecryptionKey, AND the keys the
//	                       packs were encrypted with
//
// Any path where a human types one of them twice will eventually produce
// `BcatInvalid`, which reaches the player as a generic communication error with
// nothing pointing at the mismatch. So this derives all four from one source and
// writes them to both places in one step: the seed folder for the console, and
// the fest row for the schedule.
//
// The keys are generated here rather than asked for, because a key the operator
// chose is a key they can get wrong in one of the two places.
func festBuild(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("fest build", flag.ExitOnError)
	assets := fs.String("assets", "",
		"directory of game-ready assets, one subdirectory per pack (base, bh, wa, wb, wc)")
	out := fs.String("out", "",
		"directory to write the seed folder into")
	resource := fs.String("resource", "",
		"BCAT resource name, lowercase (default: derived from the fest id)")
	festID := fs.String("id", "",
		"fest id, NXTD-00042-shaped (default: derived from the start time)")
	startAt := fs.String("start", "",
		"when play opens, RFC3339 (default: the next Saturday, midnight UTC)")
	regions := fs.String("regions", strings.Join(toyohr.DefaultRegions, ","),
		"comma-separated regions to publish in")
	enable := fs.Bool("enable", false,
		"announce the fest immediately, rather than leaving it scheduled and off")
	fs.Usage = func() { fmt.Fprint(os.Stderr, festBuildUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *assets == "" || *out == "" {
		fs.Usage()
		os.Exit(2)
	}

	// 1. Settle the fest's identity, because the in-pack asset names may
	//    contain the fest id -- see bcat.FestPlaceholder. The revision comes
	//    from the packs, so it is filled in after they are built.
	start := toyohr.NextFestStart(time.Now().UTC())
	if *startAt != "" {
		parsed, err := time.Parse(time.RFC3339, *startAt)
		if err != nil {
			fail(fmt.Errorf("-start must be RFC3339, e.g. 2026-09-05T00:00:00Z: %w", err))
		}
		start = parsed
	}
	fest := toyohr.NewFest(start, splitList(*regions))
	if *festID != "" {
		fest.ID = strings.ToUpper(strings.TrimSpace(*festID))
	}
	if *resource != "" {
		fest.Resource = strings.ToLower(strings.TrimSpace(*resource))
	}

	if !toyohr.IsSlotAligned(start) {
		// Reported rather than silently aligned, because an operator who asked
		// for 13:00 should be told it becomes 12:00. NewFest has already
		// aligned it; this is the notice.
		fmt.Fprintf(os.Stderr,
			"note: -start %s is not on a two-hour slot boundary; using %s instead\n",
			start.UTC().Format(time.RFC3339), fest.Timetable().Start.Format(time.RFC3339))
	}

	// 2. Build the packs. Their bytes decide the revision, so a revision chosen
	//    before they exist is a revision that will not match them.
	opts := bcat.BuildOptions{}
	if cfg.BCAT.SubstituteFestID {
		opts.Naming = bcat.Naming{FestID: fest.ID}
	}
	if cfg.BCAT.Converter != "" {
		conv, err := bcat.NewCommandConverter(cfg.BCAT.Converter,
			time.Duration(cfg.BCAT.ConverterTimeout)*time.Second)
		if err != nil {
			fail(err)
		}
		opts.Converter = conv
	}

	packs, err := bcat.BuildPacksWith(context.Background(), *assets, opts)
	if err != nil {
		fail(err)
	}
	fest.Revision = bcat.Revision(packs)

	// 3. One key per pack, generated. The raw bytes encrypt the pack and the
	//    hex goes in the fest row, both from bcat.KeysInFieldOrder -- so the two
	//    representations, and the field each key lands in, cannot disagree. That
	//    mapping is named rather than positional for a reason: see
	//    bcat.PackForKeyField.
	keys := make(map[string][]byte, len(packs))
	for name := range packs {
		keys[name] = randomAESKey()
	}
	hexKeys := bcat.KeysInFieldOrder(keys)

	// 4. Write the seed the console reads.
	seed := bcat.Seed{
		FestID:     fest.ID,
		ResourceID: fest.Resource,
		Revision:   fest.Revision,
		Regions:    splitList(*regions),
	}
	if err := seed.Write(*out, packs, keys); err != nil {
		fail(err)
	}

	// 5. Register the fest the schedule announces. Last, so a failure to write
	//    the seed does not leave a fest announced whose packs are not on disk --
	//    which is exactly the BcatInvalid this command exists to prevent.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		fail(fmt.Errorf("open database: %w", err))
	}
	defer func() { _ = store.Close() }()

	svc := toyohr.NewService(toyohr.Options{
		Store:  store,
		Log:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Tenant: cfg.NPLN.Tenant,
	})
	if err := svc.SaveFest(ctx, fest, *enable, hexKeys); err != nil {
		fail(err)
	}

	tt := fest.Timetable()
	fmt.Printf("built fest %s\n", fest.ID)
	fmt.Printf("  resource:  %s\n", fest.Resource)
	fmt.Printf("  revision:  %s\n", fest.Revision)
	fmt.Printf("  packs:     %s\n", strings.Join(builtPacks(packs), ", "))
	fmt.Printf("  seed:      %s\n", filepath.Join(*out, "bcat-seed"))
	fmt.Printf("  announced: %s\n", tt.Open.Format(time.RFC3339))
	fmt.Printf("  play:      %s .. %s\n",
		tt.Start.Format(time.RFC3339), tt.End.Format(time.RFC3339))
	fmt.Printf("  results:   %s\n", tt.Close.Format(time.RFC3339))
	fmt.Printf("  enabled:   %t\n", *enable)
	fmt.Println()
	fmt.Println("Copy the seed folder onto each console's SD card, under Splatoon 3's")
	fmt.Println("BCAT save data. The announced id, resource and revision above are")
	fmt.Println("already what the packs carry, so they cannot disagree.")
	if !*enable {
		fmt.Println()
		fmt.Println("The fest is scheduled but not announced. Turn it on from the")
		fmt.Println("dashboard, or re-run with -enable.")
	}
}

// randomAESKey generates a 16-byte AES-128 key.
//
// Generated rather than asked for: the same key has to encrypt the pack and come
// back from GetFestDecryptionKey, and a key an operator types twice is a key they
// can type differently once. The failure is a pack that does not open, reported
// to the player as a generic communication error.
func randomAESKey() []byte {
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		fail(fmt.Errorf("no randomness available for a pack key: %w", err))
	}
	return key[:]
}

func builtPacks(packs bcat.Packs) []string {
	var out []string
	for _, name := range bcat.PackOrder {
		if _, ok := packs[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, strings.ToUpper(part))
		}
	}
	return out
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
	os.Exit(1)
}

const festBuildUsage = `switchnetctl fest build -assets <dir> -out <dir> [options]

Builds a Splatfest's BCAT packs, writes the seed folder, and registers the
fest that announces them — in one step, so the four values that must agree
cannot drift apart.

Those four are the fest id, the resource name, the revision, and the pack
keys. Each appears in two places: the NPLN schedule the console is told, and
the packs the console holds. Any path where a human types one of them twice
eventually produces BcatInvalid, which the player sees as a generic
communication error naming nothing.

  -assets   a directory with one subdirectory per pack: base, bh, wa, wb, wc.
            Files inside must ALREADY be in the game's own formats (.bfres,
            .byaml, .sarc.zs). Converting a PNG into a game-ready texture is
            a separate and much larger problem — see TODO.md.
  -out      where to write bcat-seed/. Copy it onto each console.

  -id        fest id (default: derived from the start time)
  -resource  BCAT resource name, lowercase. Once a pack exists, its own
             summary.yml is the authority — pass it here so the announcement
             matches
  -start     when play opens, RFC3339. Must land on a two-hour boundary;
             a value that does not is moved down to one, with a notice
  -regions   comma-separated (default JP,US,EU,AP)
  -enable    announce it immediately rather than leaving it scheduled and off

The pack keys are generated, not asked for, for the same reason as above.
They are written to the fest row so GetFestDecryptionKey can return them.

Requires SWITCHNET_BCAT_KEY_MATERIAL: the IV-derivation table is lifted from
the game binary and is not in this repository. Without it this command fails
saying so, rather than producing packs that silently never open.
`
