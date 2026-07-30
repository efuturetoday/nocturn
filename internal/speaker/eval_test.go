package speaker

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"testing"
)

// The evaluation harness. It is a measurement, not an assertion: it reports where the decision
// threshold falls for a given corpus and microphone, which is a number that cannot be derived and
// must not be guessed. Absolute cosine values move with the checkpoint, the room and the microphone,
// so a threshold copied from a paper is worth very little on someone's kitchen.
//
// Corpus layout is one directory per speaker, holding 16 kHz mono 16-bit WAV files:
//
//	corpus/oliver/take-1.wav
//	corpus/oliver/take-2.wav
//	corpus/anna/take-1.wav
//
// Run it with:
//
//	NOCTURN_SPEAKER_MODEL=… NOCTURN_SPEAKER_CORPUS=… go test ./internal/speaker/ -run Evaluate -v
const (
	corpusEnv    = "NOCTURN_SPEAKER_CORPUS"
	focusEnv     = "NOCTURN_SPEAKER_FOCUS"        // report one speaker separately
	perSpeaker   = "NOCTURN_SPEAKER_MAX_TAKES"    // utterances per speaker, default 6
	maxSpeakerNo = "NOCTURN_SPEAKER_MAX_SPEAKERS" // speakers, default 40
)

type utterance struct {
	speaker   string
	path      string
	embedding []float32
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// collect gathers WAV files grouped by their parent directory, applying the caps deterministically
// so two runs over one corpus compare the same recordings.
func collect(t *testing.T, root string, maxSpeakers, maxTakes int) []utterance {
	t.Helper()

	bySpeaker := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".wav" {
			return nil
		}
		speaker := filepath.Base(filepath.Dir(path))
		bySpeaker[speaker] = append(bySpeaker[speaker], path)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	// A speaker with one recording contributes no same-speaker pair and cannot inform a threshold.
	var speakers []string
	for name, takes := range bySpeaker {
		if len(takes) >= 2 {
			speakers = append(speakers, name)
		}
	}
	slices.Sort(speakers)

	if len(speakers) > maxSpeakers {
		t.Logf("corpus holds %d usable speakers; using the first %d (raise %s to widen)",
			len(speakers), maxSpeakers, maxSpeakerNo)
		speakers = speakers[:maxSpeakers]
	}

	var out []utterance
	dropped := 0
	for _, s := range speakers {
		takes := bySpeaker[s]
		sort.Strings(takes)
		if len(takes) > maxTakes {
			dropped += len(takes) - maxTakes
			takes = takes[:maxTakes]
		}
		for _, p := range takes {
			out = append(out, utterance{speaker: s, path: p})
		}
	}
	if dropped > 0 {
		t.Logf("skipped %d recordings beyond %d per speaker (raise %s to include them)",
			dropped, maxTakes, perSpeaker)
	}
	return out
}

// equalErrorRate sweeps the decision threshold and returns the point where the two ways of being
// wrong balance: rejecting a genuine match, and accepting an impostor. It is the standard summary
// of a verification system, and the threshold that produces it is the one to ship as a default.
func equalErrorRate(same, different []float32) (eer, threshold float64) {
	slices.Sort(same)
	slices.Sort(different)

	best := math.Inf(1)
	for _, cut := range slices.Concat(same, different) {
		// Genuine pairs scoring below the cut are rejected; impostor pairs at or above it accepted.
		falseReject := float64(sort.Search(len(same), func(i int) bool { return same[i] >= cut }))
		falseAccept := float64(len(different) - sort.Search(len(different), func(i int) bool {
			return different[i] >= cut
		}))
		frr := falseReject / float64(len(same))
		far := falseAccept / float64(len(different))
		if gap := math.Abs(frr - far); gap < best {
			best, eer, threshold = gap, (frr+far)/2, float64(cut)
		}
	}
	return eer, threshold
}

func summarize(scores []float32) (mean, stddev float64) {
	for _, s := range scores {
		mean += float64(s)
	}
	mean /= float64(len(scores))
	for _, s := range scores {
		d := float64(s) - mean
		stddev += d * d
	}
	return mean, math.Sqrt(stddev / float64(len(scores)))
}

func TestEvaluateCorpus(t *testing.T) {
	modelPath := os.Getenv("NOCTURN_SPEAKER_MODEL")
	corpus := os.Getenv(corpusEnv)
	if modelPath == "" || corpus == "" {
		t.Skipf("set %s and %s to measure the decision threshold", "NOCTURN_SPEAKER_MODEL", corpusEnv)
	}

	e, err := Open(modelPath)
	if err != nil {
		t.Fatalf("opening model: %v", err)
	}

	utterances := collect(t, corpus, envInt(maxSpeakerNo, 40), envInt(perSpeaker, 6))
	if len(utterances) < 4 {
		t.Fatalf("found %d usable recordings; need at least two speakers with two each", len(utterances))
	}

	skipped := 0
	kept := utterances[:0]
	for _, u := range utterances {
		pcm, err := ReadWAV(u.path)
		if err != nil {
			t.Fatalf("%v", err) // a malformed file is a setup error, not a result
		}
		if len(pcm) < MinSamples {
			skipped++
			continue
		}
		if u.embedding, err = e.Embed(pcm); err != nil {
			t.Fatalf("embedding %s: %v", u.path, err)
		}
		kept = append(kept, u)
	}
	if skipped > 0 {
		t.Logf("skipped %d recordings shorter than %d samples", skipped, MinSamples)
	}

	speakers := map[string]int{}
	for _, u := range kept {
		speakers[u.speaker]++
	}
	t.Logf("%d recordings from %d speakers", len(kept), len(speakers))

	var same, different []float32
	for i := range kept {
		for j := i + 1; j < len(kept); j++ {
			s, err := Similarity(kept[i].embedding, kept[j].embedding)
			if err != nil {
				t.Fatal(err)
			}
			if kept[i].speaker == kept[j].speaker {
				same = append(same, s)
			} else {
				different = append(different, s)
			}
		}
	}
	if len(same) == 0 || len(different) == 0 {
		t.Fatalf("built %d genuine and %d impostor pairs; need both", len(same), len(different))
	}

	sameMean, sameSD := summarize(same)
	diffMean, diffSD := summarize(different)
	eer, threshold := equalErrorRate(same, different)

	t.Logf("genuine pairs   %5d   similarity %.4f ± %.4f", len(same), sameMean, sameSD)
	t.Logf("impostor pairs  %5d   similarity %.4f ± %.4f", len(different), diffMean, diffSD)
	t.Logf("equal error rate %.2f%% at threshold %.4f", eer*100, threshold)
	t.Logf("worst genuine %.4f, best impostor %.4f",
		slices.Min(same), slices.Max(different))

	// One speaker on their own, when asked for. The aggregate is dominated by whoever is most
	// numerous in the corpus, so a single voice recorded through a different microphone — the case
	// worth knowing about — disappears into it. This is how that voice is looked at directly.
	if focus := os.Getenv(focusEnv); focus != "" {
		var mine, theirs []float32
		for i := range kept {
			for j := range kept {
				if i == j || (kept[i].speaker != focus && kept[j].speaker != focus) {
					continue
				}
				if j < i && kept[i].speaker == kept[j].speaker {
					continue // one direction is enough within the focused speaker
				}
				if kept[j].speaker == focus && kept[i].speaker != focus {
					continue // counted from the focused side already
				}
				s, err := Similarity(kept[i].embedding, kept[j].embedding)
				if err != nil {
					t.Fatal(err)
				}
				if kept[i].speaker == kept[j].speaker {
					mine = append(mine, s)
				} else {
					theirs = append(theirs, s)
				}
			}
		}
		if len(mine) > 0 && len(theirs) > 0 {
			mineMean, mineSD := summarize(mine)
			theirsMean, theirsSD := summarize(theirs)
			t.Logf("focus %q — own pairs      %4d  similarity %.4f ± %.4f (worst %.4f)",
				focus, len(mine), mineMean, mineSD, slices.Min(mine))
			t.Logf("focus %q — against others %4d  similarity %.4f ± %.4f (best  %.4f)",
				focus, len(theirs), theirsMean, theirsSD, slices.Max(theirs))
			t.Logf("focus %q — margin between worst own and best other: %.4f",
				focus, slices.Min(mine)-slices.Max(theirs))
		}
	}

	// These figures describe open-set verification against a world of strangers, which is the
	// standard way to characterise a checkpoint but NOT the question a household asks. See
	// TestEvaluateHousehold for the number that decides how this behaves in a kitchen.
	t.Log("threshold   false accept   false reject")
	for _, cut := range []float32{0.35, 0.40, 0.45, 0.50, 0.55, 0.60, 0.65, 0.70} {
		var accepted, rejected int
		for _, s := range different {
			if s >= cut {
				accepted++
			}
		}
		for _, s := range same {
			if s < cut {
				rejected++
			}
		}
		t.Logf("   %.2f      %6.3f%% (%4d)   %6.3f%% (%3d)",
			cut,
			100*float64(accepted)/float64(len(different)), accepted,
			100*float64(rejected)/float64(len(same)), rejected)
	}

	// The one thing worth failing on: if the distributions do not separate at all, the pipeline is
	// broken rather than merely imprecise. Everything finer is a number to read, not a gate.
	if sameMean <= diffMean {
		t.Errorf("genuine pairs average %.4f and impostor pairs %.4f; the pipeline does not separate speakers",
			sameMean, diffMean)
	}
	if eer > 0.25 {
		t.Errorf("equal error rate %.2f%% is far worse than this model should achieve; "+
			"suspect the frontend rather than the corpus", eer*100)
	}

}
