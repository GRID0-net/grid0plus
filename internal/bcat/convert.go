package bcat

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Asset conversion: turning an operator's source files into the formats
// Splatoon 3 reads.
//
// # Why this is a contract rather than an implementation
//
// A Splatfest's splashart, tee designs and banners are textures inside BFRES
// containers: BC1/BC3/BC7-compressed, in the Tegra GPU's block-linear memory
// layout, wrapped in one of Nintendo's own binary formats. Its team names and
// idol dialogue are BYAML documents.
//
// Reimplementing that chain in Go would be a large piece of work whose failure
// mode is the worst available: a texture that LOADS and renders as garbage. Not
// a build error, not a refusal — noise on screen, on a console, with nothing on
// the server to look at. Switch Toolbox and the BinaryTextureLibrary /
// Syroot.NintenTools family already do this correctly, and they exist because it
// is hard.
//
// So SwitchNet does not convert. It defines where conversion happens, invokes
// whatever the operator points it at, checks that something came out, and — most
// importantly — **refuses to build a pack containing a file the game cannot
// read**. That last part is the change that matters: before it, a PNG left in an
// assets directory went straight into the archive and the fest failed on the
// console with nothing pointing at the file.
//
// TODO.md §2.1 has the rest: what a native implementation would need, and why
// vendoring an existing converter is likely to beat writing one.

// SourceSuffix is the extension that marks a file as needing conversion.
//
// The rule is deliberately one line: **a source file is named for what it must
// become, with the source extension appended.**
//
//	UI/Splashart.bfres.png   ->  UI/Splashart.bfres
//	Mals/Msg_EUen.byml.yaml  ->  Mals/Msg_EUen.byml
//
// So the operator names the target and the pipeline never has to guess it, there
// is no separate manifest to keep in step with the filenames, and the target
// format is visible in a directory listing.
var SourceSuffix = map[string]string{
	".png":  "image",
	".jpg":  "image",
	".jpeg": "image",
	".yaml": "text",
	".yml":  "text",
	".json": "text",
}

// FestPlaceholder is substituted with the fest id in an asset's path inside the
// pack.
//
// # Why this exists
//
// A real custom pack read during this project's research had every file inside
// named for the fest it was DERIVED from, not the fest it announces:
//
//	Mals/Msg_EUde.Fest_JUEA-DUMPLING_Bh.sarc.zs
//	RSDB/Tag.Fest_JUEA-DUMPLING.dev.rstbl.byml.zs
//
// while its summary.yml announced JUEA-00210. **Nobody has established what the
// game keys on.** If it resolves assets by the announced fest id, a
// from-scratch fest needs every internal path renamed consistently; if it only
// reads the pack it was handed, the names are inert.
//
// This placeholder is that decision in ONE place. An operator who writes
// `Mals/Msg_EUen.Fest_{FEST}_Bh.sarc.zs` gets consistent naming for free; one
// who writes a literal name gets it left alone. Whichever way the question is
// answered, acting on the answer is a change to Naming.Apply and nothing else.
//
// The experiment, and the assumption being proceeded on, are recorded in
// docs/QUESTIONS.md. It needs a console.
const FestPlaceholder = "{FEST}"

// Naming decides the path an asset gets inside a pack.
type Naming struct {
	// FestID is substituted for FestPlaceholder. Empty leaves the placeholder
	// in place, which is a visible failure rather than a silent one: a file
	// literally called Fest_{FEST}_Bh is obviously wrong in a listing.
	FestID string
}

// Apply returns the in-pack path for a source file's pack-relative path.
func (n Naming) Apply(path string) string {
	if n.FestID == "" {
		return path
	}
	return strings.ReplaceAll(path, FestPlaceholder, n.FestID)
}

// Converter turns a source asset into a game-format file.
type Converter interface {
	// Convert reads src and writes the converted file to dst.
	//
	// kind is SourceSuffix's classification ("image", "text"), and target is
	// the extension dst is expected to be — both passed so one command can
	// dispatch rather than needing a converter per format.
	Convert(ctx context.Context, src, dst, kind, target string) error

	// Describe says what this converter is, for the error an operator sees when
	// a pack needs conversion and cannot get it.
	Describe() string
}

// CommandConverter shells out to a command the operator configured.
//
// The command is given the source path, the destination path, the kind and the
// target extension, in that order. It must write the destination file and exit
// zero; anything else is reported with the exact command line, because a
// conversion that half-worked is much harder to diagnose from the resulting pack
// than from the command that produced it.
type CommandConverter struct {
	// Command is the executable and its leading arguments.
	Command []string
	// Timeout bounds one conversion. A converter that hangs would otherwise
	// hang the build, and on the dashboard that means a request that never
	// answers.
	Timeout time.Duration
}

// NewCommandConverter parses a configured command line.
func NewCommandConverter(command string, timeout time.Duration) (*CommandConverter, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil, fmt.Errorf("bcat: no converter command")
	}
	// Resolved now rather than at conversion time, so a missing tool is an
	// error when the server starts rather than when an operator is halfway
	// through building a fest.
	if _, err := exec.LookPath(fields[0]); err != nil {
		return nil, fmt.Errorf("bcat: converter %q: %w", fields[0], err)
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &CommandConverter{Command: fields, Timeout: timeout}, nil
}

// Describe implements Converter.
func (c *CommandConverter) Describe() string {
	return strings.Join(c.Command, " ")
}

// Convert implements Converter.
func (c *CommandConverter) Convert(ctx context.Context, src, dst, kind, target string) error {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	args := append(append([]string(nil), c.Command[1:]...), src, dst, kind, target)
	cmd := exec.CommandContext(ctx, c.Command[0], args...)
	// The command's own output is the only diagnostic there will be, so it is
	// carried into the error rather than discarded.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bcat: convert %s: %s %s: %w\n%s",
			filepath.Base(src), c.Command[0], strings.Join(args, " "), err, out)
	}

	info, statErr := os.Stat(dst)
	if statErr != nil {
		// Exit zero and no output file. Caught here because the alternative is
		// a pack silently missing a file, which on the console is a fest with
		// an invisible splashart and nothing to say why.
		return fmt.Errorf("bcat: convert %s: the converter exited zero but wrote no %s\n%s",
			filepath.Base(src), filepath.Base(dst), out)
	}
	if info.Size() == 0 {
		return fmt.Errorf("bcat: convert %s: the converter wrote an empty %s\n%s",
			filepath.Base(src), filepath.Base(dst), out)
	}
	return nil
}

// convertSource returns the in-pack name a source file becomes, and its kind.
//
// A file with no source suffix is not a source file: it is already in a game
// format and passes through untouched.
func convertSource(path string) (target, kind string, isSource bool) {
	ext := strings.ToLower(filepath.Ext(path))
	kind, ok := SourceSuffix[ext]
	if !ok {
		return "", "", false
	}
	target = strings.TrimSuffix(path, filepath.Ext(path))
	if filepath.Ext(target) == "" {
		// "splashart.png" with nothing before the extension. The rule is that
		// the name states the target, and this one does not — refused by the
		// caller rather than guessed at, because guessing produces a file the
		// game will not recognise and a fest that fails on the console.
		return "", kind, true
	}
	return target, kind, true
}
