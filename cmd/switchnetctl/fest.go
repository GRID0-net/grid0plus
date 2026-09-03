package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/bcat"
	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/toyohr"
)

// festBuild implements `switchnetctl fest build`.
//
// It exists so that a fest's id, resource name, pack revision and decryption
// keys cannot drift apart: today those four values are typed by hand into
// separate places (the assets directory, the dashboard's Splatfest form, the
// seed folder a script copies onto an SD card), and any path where a human
// types one of them twice eventually produces BcatInvalid -- which reaches
// the player as a generic communication error with nothing pointing at which
// of the four disagreed.
//
// This command builds the packs, derives the revision from their own bytes
// (never typed), generates one fresh key per pack, writes the seed folder
// with those keys, and registers the fest with the exact same id, resource,
// revision and keys -- so there is exactly one place any of them is decided.
func festBuild(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("fest build", flag.ExitOnError)
	assetsDir := fs.String("assets", "",
		"directory of game-ready assets, one subdirectory per pack (base/bh/wa/wb/wc) -- see internal/bcat's package comment")
	festID := fs.String("fest-id", "",
		"fest id, e.g. NXTD-00042 (default: derived from -start, the same way the dashboard's 'new fest' does)")
	resource := fs.String("resource", "",
		"lowercase BCAT pack identifier the console's decryption IV is derived from (default: derived from the fest id)")
	startStr := fs.String("start", "",
		"fest start time, RFC3339 (e.g. 2026-09-06T00:00:00Z) -- rounded down to the nearest two-hour slot")
	regionsStr := fs.String("regions", strings.Join(toyohr.DefaultRegions, ","),
		"comma-separated regions to publish in")
	seedDir := fs.String("seed-dir", "",
		"directory to write the bcat-seed/ folder into, for installing onto a console's save data")
	enabled := fs.Bool("enabled", false,
		"announce the fest immediately, rather than saving it scheduled but hidden")
	sourceImages := fs.String("source-images", "",
		"optional: a directory laid out like -assets, but a .png where -assets would want a "+
			"converted texture -- everything else is copied through unchanged. Converted into "+
			"-assets before building. See -texture-converter and internal/bcat's README.")
	textureConverter := fs.String("texture-converter", "",
		"path to a PNG-to-BNTX converter binary following jam1garner/img2bntx's CLI contract, "+
			"required only if -source-images contains a .png")
	fs.Usage = func() { fmt.Fprint(os.Stderr, festBuildUsage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *assetsDir == "" || *startStr == "" || *seedDir == "" {
		fs.Usage()
		os.Exit(2)
	}

	start, err := time.Parse(time.RFC3339, *startStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: -start %q: %v (want RFC3339, e.g. 2026-09-06T00:00:00Z)\n", *startStr, err)
		os.Exit(2)
	}
	start = toyohr.AlignToSlot(start)

	id := strings.ToUpper(*festID)
	if id == "" {
		id = toyohr.NewFestID(start)
	}
	res := strings.ToLower(*resource)
	if res == "" {
		res = toyohr.NewResource(id)
	}
	var regions []string
	for _, r := range strings.Split(*regionsStr, ",") {
		if r = strings.TrimSpace(r); r != "" {
			regions = append(regions, r)
		}
	}

	if *sourceImages != "" {
		converted, err := bcat.ConvertTextures(*textureConverter, *sourceImages, *assetsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "switchnetctl: convert textures: %v\n", err)
			os.Exit(1)
		}
		if len(converted) > 0 {
			fmt.Printf("converted %d texture(s) from %s into %s\n", len(converted), *sourceImages, *assetsDir)
		}
	}

	packs, err := bcat.BuildPacks(*assetsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}
	revision := bcat.Revision(packs)

	seedKeys, packKeys, err := generatePackKeys(packs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}

	seed := bcat.Seed{FestID: id, ResourceID: res, Revision: revision, Regions: regions}
	if err := seed.Write(*seedDir, packs, seedKeys); err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: open database: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	f := toyohr.Fest{ID: id, Resource: res, Revision: revision, Start: start, Regions: regions}
	svc := toyohr.NewService(store, slog.Default(), cfg.NPLN.Tenant, time.Now)
	if err := svc.SaveFest(ctx, f, *enabled, packKeys); err != nil {
		fmt.Fprintf(os.Stderr, "switchnetctl: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("built and registered fest %s\n", id)
	fmt.Println()
	fmt.Printf("  resource:  %s\n", res)
	fmt.Printf("  revision:  %s\n", revision)
	fmt.Printf("  regions:   %s\n", strings.Join(regions, ", "))
	fmt.Printf("  start:     %s\n", start.Format(time.RFC3339))
	fmt.Printf("  packs:     %s\n", strings.Join(packNames(packs), ", "))
	fmt.Printf("  enabled:   %v\n", *enabled)
	fmt.Printf("  seed dir:  %s\n", *seedDir)
	fmt.Println()
	if !*enabled {
		fmt.Println("Saved but not announced -- schedule shows it, GetFestSchedule does not,")
		fmt.Println("until it is enabled (dashboard, or this command again with -enabled).")
	}
	fmt.Printf("Install %s/bcat-seed/ onto the console's Splatoon 3 save data before it\n", *seedDir)
	fmt.Println("is announced, or the console asks for a pack it does not have yet.")
}

// generatePackKeys makes one fresh AES-128 key per pack that was actually
// built, positioned in bcat.PackOrder -- the same order GetFestDecryptionKey
// hands keys back in (see internal/toyohr/service.go's assignPackKeys). A
// pack this fest does not have (a two-team fest has no wc) leaves its slot
// "" in packKeys, which assignPackKeys treats as absent rather than as a key
// of "" the console would try and fail to use. seedKeys, keyed by pack name,
// is what bcat.Seed.Write encrypts with; packKeys, positional, is what
// toyohr.Service.SaveFest stores for GetFestDecryptionKey to hand back --
// both built from the same bytes, so the two can never disagree.
func generatePackKeys(packs bcat.Packs) (seedKeys map[string][]byte, packKeys []string, err error) {
	seedKeys = make(map[string][]byte, len(packs))
	packKeys = make([]string, 0, len(bcat.PackOrder))
	for _, name := range bcat.PackOrder {
		if _, ok := packs[name]; !ok {
			packKeys = append(packKeys, "")
			continue
		}
		key := make([]byte, 16)
		if _, err := rand.Read(key); err != nil {
			return nil, nil, fmt.Errorf("generate key for pack %q: %w", name, err)
		}
		seedKeys[name] = key
		packKeys = append(packKeys, hex.EncodeToString(key))
	}
	return seedKeys, packKeys, nil
}

// packNames returns a Packs map's keys in bcat.PackOrder.
func packNames(packs bcat.Packs) []string {
	var out []string
	for _, name := range bcat.PackOrder {
		if _, ok := packs[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

const festBuildUsage = `switchnetctl fest build -assets <dir> -start <RFC3339 time> -seed-dir <dir> [-fest-id <id>] [-resource <name>] [-regions JP,US,EU,AP] [-enabled] [-source-images <dir> -texture-converter <path>]

Builds a Splatfest's BCAT packs from game-ready assets, derives the pack
revision from their own bytes, generates fresh decryption keys, writes the
seed folder those keys were used with, and registers the fest with the same
id, resource, revision and keys -- so the four values that all have to agree
for the console to load the fest cannot be typed twice and drift apart.

-assets must contain one subdirectory per pack this fest has (base/bh/wa/wb/wc),
each holding files already in the game's own formats (.bfres, .byaml,
.sarc.zs and so on) -- see internal/bcat's package comment for the layout.

To build -assets FROM a directory of PNGs instead of authoring them by hand,
pass -source-images (laid out the same way) and -texture-converter (a
PNG-to-BNTX tool following jam1garner/img2bntx's CLI contract -- see
internal/bcat's README for exactly what was verified about it and what was
not). This only ever produces a standalone .bntx per image; it does not
author a .bfres, and it does not touch team names or dialogue in a .byaml --
see TODO.md §2.1 for what "asset conversion" still means beyond textures.

-fest-id and -resource default to the same derivation
'switchnetctl fest build' relies on internally (toyohr.NewFestID/NewResource)
when the fest's pack has never existed before. Pass them explicitly when a
pack under this name already exists elsewhere -- the announced resource MUST
equal the one in that pack's own summary.yml, or the console looks for
content it cannot find.

Requires SWITCHNET_BCAT_KEY_MATERIAL (the IV-derivation table, lifted from
the game binary and never in this repository) and a reachable database --
see 'internal/bcat' and 'switchnetctl account create -h' for both.
`
