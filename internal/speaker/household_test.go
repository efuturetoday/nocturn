package speaker

import (
	"math/rand/v2"
	"os"
	"slices"
	"testing"
)

// The question a household actually asks.
//
// TestEvaluateCorpus measures open-set verification: is this Oliver, yes or no, against a world of
// strangers. That is how a checkpoint is characterised in the literature, and it is the wrong shape
// for a flat with three people in it. There the question is closed-set identification — of these
// three, who just spoke — and it is a far easier problem, because the answer only has to beat two
// alternatives rather than everyone alive.
//
// The consequence is that the operating point differs. Getting it wrong costs a misaddressed
// sentence or the wrong set of notes in a prompt; nothing hangs on it, because the identity carries
// no authority (see the package documentation). An assistant that keeps announcing it cannot tell
// who is speaking is worse company than one that occasionally guesses wrong, so the threshold
// belongs low — its only job is catching the visitor and the television, not gatekeeping.

// profile is one enrolled person: several embeddings rather than one averaged centroid.
type profile struct {
	name  string
	takes [][]float32
}

// score rates a query against a profile two ways, to settle which enrolment shape is better with
// numbers rather than intuition.
//
// centroid averages the enrolled takes into one vector — compact, but it averages away genuine
// variation, and a person recorded on two devices lands at a point matching neither.
// best takes the highest similarity to any single take, which keeps each recording's character.
func (p profile) score(query []float32) (centroid, best float32) {
	mean := make([]float32, len(query))
	for _, take := range p.takes {
		for i, v := range take {
			mean[i] += v
		}
	}
	for i := range mean {
		mean[i] /= float32(len(p.takes))
	}
	// Similarity assumes unit length, which the mean of unit vectors is not.
	centroid = cosine(Normalize(mean), query)

	for _, take := range p.takes {
		if s := cosine(take, query); s > best {
			best = s
		}
	}
	return centroid, best
}

func cosine(a, b []float32) float32 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return float32(dot)
}

type outcome struct {
	trials, correctCentroid, correctBest int
	insiderScores, guestScores           []float32
}

// simulate draws random households from the corpus and asks, for every held-out recording of a
// member, which profile it matches best. It also asks the same of a non-member, since a household
// assistant meets visitors and television.
func simulate(bySpeaker map[string][][]float32, size, enrol, trials int, seed uint64) outcome {
	names := slices.Sorted(maps(bySpeaker))
	r := rand.New(rand.NewPCG(seed, seed+1))
	var out outcome

	for range trials {
		r.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
		if len(names) < size+1 { // one spare for the visitor
			return out
		}

		household := make([]profile, size)
		var queries []struct {
			who       string
			embedding []float32
		}
		for i, name := range names[:size] {
			takes := bySpeaker[name]
			household[i] = profile{name: name, takes: takes[:enrol]}
			for _, held := range takes[enrol:] {
				queries = append(queries, struct {
					who       string
					embedding []float32
				}{name, held})
			}
		}

		identify := func(query []float32) (byCentroid, byBest string, bestScore float32) {
			var topCentroid, topBest float32 = -2, -2
			for _, p := range household {
				c, b := p.score(query)
				if c > topCentroid {
					topCentroid, byCentroid = c, p.name
				}
				if b > topBest {
					topBest, byBest = b, p.name
				}
			}
			return byCentroid, byBest, topBest
		}

		for _, q := range queries {
			byCentroid, byBest, top := identify(q.embedding)
			out.trials++
			if byCentroid == q.who {
				out.correctCentroid++
			}
			if byBest == q.who {
				out.correctBest++
			}
			out.insiderScores = append(out.insiderScores, top)
		}

		// The visitor: someone the household never enrolled.
		for _, held := range bySpeaker[names[size]][enrol:] {
			_, _, top := identify(held)
			out.guestScores = append(out.guestScores, top)
		}
	}
	return out
}

func maps(m map[string][][]float32) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func TestEvaluateHousehold(t *testing.T) {
	modelPath := os.Getenv("NOCTURN_SPEAKER_MODEL")
	corpus := os.Getenv(corpusEnv)
	if modelPath == "" || corpus == "" {
		t.Skipf("set %s and %s to measure household identification", "NOCTURN_SPEAKER_MODEL", corpusEnv)
	}

	e, err := Open(modelPath)
	if err != nil {
		t.Fatalf("opening model: %v", err)
	}

	const enrol = 3 // three enrolment recordings, the rest held out as queries
	bySpeaker := map[string][][]float32{}
	for _, u := range collect(t, corpus, envInt(maxSpeakerNo, 40), envInt(perSpeaker, 6)) {
		pcm, err := readWAV(u.path)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if len(pcm) < MinSamples {
			continue
		}
		emb, err := e.Embed(pcm)
		if err != nil {
			t.Fatalf("embedding %s: %v", u.path, err)
		}
		bySpeaker[u.speaker] = append(bySpeaker[u.speaker], emb)
	}
	for name, takes := range bySpeaker {
		if len(takes) <= enrol {
			delete(bySpeaker, name) // no held-out recording, so nothing to ask
		}
	}
	if len(bySpeaker) < 3 {
		t.Fatalf("%d speakers have more than %d recordings; need at least 3", len(bySpeaker), enrol)
	}
	t.Logf("%d speakers, %d enrolment recordings each, the rest held out", len(bySpeaker), enrol)

	t.Log("household   top-1 (centroid)   top-1 (best-of-takes)   queries")
	for _, size := range []int{2, 3, 4, 5, 6} {
		if size+1 > len(bySpeaker) {
			break
		}
		got := simulate(bySpeaker, size, enrol, 200, uint64(size))
		if got.trials == 0 {
			continue
		}
		centroidRate := 100 * float64(got.correctCentroid) / float64(got.trials)
		bestRate := 100 * float64(got.correctBest) / float64(got.trials)
		t.Logf("   %d          %6.2f%%             %6.2f%%            %5d",
			size, centroidRate, bestRate, got.trials)

		// Chance would be 100/size percent. Anything near that means the pipeline is broken.
		if bestRate < 100/float64(size)*2 {
			t.Errorf("household of %d identified correctly only %.2f%% of the time", size, bestRate)
		}
	}

	// How a visitor scores against a household of four, which is what a reject threshold has to
	// separate. Members should sit far above it.
	got := simulate(bySpeaker, 4, enrol, 200, 99)
	slices.Sort(got.insiderScores)
	slices.Sort(got.guestScores)
	t.Logf("household of 4 — member best-match  median %.4f, 5th percentile %.4f",
		percentile(got.insiderScores, 0.50), percentile(got.insiderScores, 0.05))
	t.Logf("household of 4 — visitor best-match median %.4f, 95th percentile %.4f",
		percentile(got.guestScores, 0.50), percentile(got.guestScores, 0.95))

	t.Log("reject threshold   members turned away   visitors taken for a member")
	for _, cut := range []float32{0.20, 0.25, 0.30, 0.35, 0.40, 0.50} {
		turnedAway := 0
		for _, s := range got.insiderScores {
			if s < cut {
				turnedAway++
			}
		}
		mistaken := 0
		for _, s := range got.guestScores {
			if s >= cut {
				mistaken++
			}
		}
		t.Logf("      %.2f              %6.2f%%                  %6.2f%%",
			cut,
			100*float64(turnedAway)/float64(len(got.insiderScores)),
			100*float64(mistaken)/float64(len(got.guestScores)))
	}
}

func percentile(sorted []float32, p float64) float32 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	return sorted[i]
}
