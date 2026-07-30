package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/efuturetoday/nocturn/internal/speaker"
)

// Managing the voices a workspace can recognise.
//
// It touches one file — the workspace's voices.json — rather than opening a whole workspace, because
// that is all enrolment is: turn some recordings into vectors and write them down. Nothing here talks
// to a running daemon, and a daemon started afterwards picks the file up.

func cmdVoices(args []string) int {
	if len(args) == 0 {
		voicesUsage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "ls":
		return cmdVoicesLs(args[1:])
	case "add":
		return cmdVoicesAdd(args[1:])
	case "rm":
		return cmdVoicesRm(args[1:])
	case "help", "-h", "--help":
		voicesUsage(os.Stdout)
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown voices command: %s\n", args[0])
	voicesUsage(os.Stderr)
	return 2
}

func voicesUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  nocturn voices ls [-w workspace]                       Who this workspace can recognise
  nocturn voices add [--device d] [-w ws] <name> <wav…>  Enrol a voice from recordings
  nocturn voices rm <name> [-w workspace]                Forget a voice entirely

Recordings must be 16 kHz mono 16-bit WAV — the format the satellite's uplink already produces,
so files written by NOCTURN_VOICE_CAPTURE need no conversion. A directory is read for *.wav.

Flags come before the name, as everywhere else the standard flag package is used.

Embedding needs a model: set NOCTURN_SPEAKER_MODEL to a WeSpeaker ResNet34 checkpoint.
`)
}

// openVoices reaches the profile file of one workspace directly.
//
// The workspace must already exist. Creating it here would leave behind a directory holding nothing
// but voices.json, which is not a workspace and would only confuse the next thing to open it; and a
// typo'd -w would silently enrol into a workspace nobody ever uses.
func openVoices(ws string) (*speaker.Profiles, error) {
	if ws == "" {
		ws = "main"
	}
	dir := filepath.Join(wsRoot, ws)
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("no workspace %q at %s — run nocturn once to create it", ws, dir)
	}
	return speaker.OpenProfiles(filepath.Join(dir, "voices.json"))
}

func cmdVoicesLs(args []string) int {
	fs := flag.NewFlagSet("voices ls", flag.ContinueOnError)
	ws := fs.String("w", "main", "workspace")
	fs.StringVar(ws, "workspace", "main", "workspace")
	fs.Usage = usage(fs, "voices ls [-w workspace]", "List the voices this workspace can recognise.")
	if code, done := parseFlags(fs, args); done {
		return code
	}

	profiles, err := openVoices(*ws)
	if err != nil {
		fmt.Fprintln(os.Stderr, "voices:", err)
		return 1
	}
	names := profiles.Names()
	if len(names) == 0 {
		fmt.Println("nobody enrolled — nocturn voices add <name> <recordings…>")
		return 0
	}
	slices.Sort(names)
	for _, n := range names {
		fmt.Println(n)
	}
	return 0
}

func cmdVoicesAdd(args []string) int {
	fs := flag.NewFlagSet("voices add", flag.ContinueOnError)
	ws := fs.String("w", "main", "workspace")
	fs.StringVar(ws, "workspace", "main", "workspace")
	device := fs.String("device", "", "which device recorded these, e.g. satellite or phone")
	fs.Usage = usage(fs, "voices add [--device satellite] [-w workspace] <name> <files or directories…>",
		"Enrol a voice from 16 kHz mono WAV recordings.")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	if fs.NArg() < 2 {
		fs.Usage()
		return 2
	}
	name, paths := fs.Arg(0), fs.Args()[1:]

	// The device is kept with every take, because a voice enrolled on a phone and recognised through
	// a hallway speaker is two channels. Refusing to guess it here is what keeps that honest.
	if *device == "" {
		fmt.Fprintln(os.Stderr, "voices add: --device is required (which device recorded these)")
		return 2
	}

	modelPath := os.Getenv("NOCTURN_SPEAKER_MODEL")
	if modelPath == "" {
		fmt.Fprintln(os.Stderr, "voices add: set NOCTURN_SPEAKER_MODEL to a speaker-embedding checkpoint")
		return 1
	}
	embedder, err := speaker.Open(modelPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "voices add:", err)
		return 1
	}

	files, err := wavsIn(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, "voices add:", err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "voices add: no .wav files found")
		return 1
	}

	var vectors [][]float32
	var skipped int
	for _, f := range files {
		pcm, err := speaker.ReadWAV(f)
		if err != nil {
			// One unreadable file should not lose the rest, but it must be said: a silently
			// skipped recording is a profile that is thinner than the operator believes.
			fmt.Fprintf(os.Stderr, "  skipped %s: %v\n", filepath.Base(f), err)
			skipped++
			continue
		}
		embedding, err := embedder.Embed(pcm)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skipped %s: %v\n", filepath.Base(f), err)
			skipped++
			continue
		}
		vectors = append(vectors, embedding)
		fmt.Printf("  %-40s %.1f s\n", filepath.Base(f), float64(len(pcm))/speaker.SampleRate)
	}
	if len(vectors) == 0 {
		fmt.Fprintln(os.Stderr, "voices add: nothing usable to enrol")
		return 1
	}

	profiles, err := openVoices(*ws)
	if err != nil {
		fmt.Fprintln(os.Stderr, "voices add:", err)
		return 1
	}
	if err := profiles.Enrol(name, *device, vectors...); err != nil {
		fmt.Fprintln(os.Stderr, "voices add:", err)
		return 1
	}

	fmt.Printf("enrolled %s from %d recordings via %s", name, len(vectors), *device)
	if skipped > 0 {
		fmt.Printf(" (%d skipped)", skipped)
	}
	fmt.Println()
	return 0
}

func cmdVoicesRm(args []string) int {
	fs := flag.NewFlagSet("voices rm", flag.ContinueOnError)
	ws := fs.String("w", "main", "workspace")
	fs.StringVar(ws, "workspace", "main", "workspace")
	fs.Usage = usage(fs, "voices rm <name> [-w workspace]", "Forget a voice entirely.")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	profiles, err := openVoices(*ws)
	if err != nil {
		fmt.Fprintln(os.Stderr, "voices:", err)
		return 1
	}
	if err := profiles.Forget(fs.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "voices rm:", err)
		return 1
	}
	fmt.Printf("forgot %s\n", fs.Arg(0))
	return 0
}

// wavsIn expands paths: a directory contributes its *.wav, a file contributes itself.
func wavsIn(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".wav") {
				out = append(out, filepath.Join(p, e.Name()))
			}
		}
	}
	slices.Sort(out) // deterministic, so two runs over one directory enrol the same order
	return out, nil
}
