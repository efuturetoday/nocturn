package knowledge

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"
)

const (
	// bm25K1 controls how quickly repeating a term stops helping. 1.2 is the standard value: a term
	// appearing five times says more than once, and much less than five times as much.
	bm25K1 = 1.2

	// bm25B is how hard a long passage is penalised for its length. 0.75 is standard. It matters here
	// because chunks are deliberately similar in size, so this mostly corrects the ones that overshot.
	bm25B = 0.75

	// rrfK damps the top of each ranking before fusing. 60 is the value from the original reciprocal
	// rank fusion work, and the reason for the whole approach: it needs only the ORDER each ranker
	// produced, never its score. A cosine of 0.82 and a BM25 of 11.4 have no common scale, and any
	// attempt to normalise them invents one.
	rrfK = 60

	// candidates is how deep each ranker is considered before fusion. Fusion can only promote what it
	// was shown, so this is deliberately far past any limit a caller asks for.
	candidates = 50
)

// Result is one passage the search found.
type Result struct {
	Chunk
	// Score is the fused rank score. Comparable within one result list and meaningless outside it —
	// it is a sum of reciprocal ranks, not a similarity, and deliberately not presented as one.
	Score float64
	// Semantic and Lexical are the 1-based ranks this passage held in each ranker, or 0 when it did
	// not make that ranker's shortlist. Carried so a bad result can be diagnosed as "the vectors
	// liked it and the words did not" rather than guessed at.
	Semantic int
	Lexical  int
	// Similarity is the cosine between the query and this passage, always computed. UNLIKE Score it
	// means something on its own, which is why it is the number handed to the model: retrieval can
	// only rank what it has, so the judgement of whether the best match is actually an answer needs
	// the context of the question — and that is not in this package.
	//
	// It is not a probability. Its range is model-dependent: for many embedders unrelated text sits
	// well above zero, so the useful reading is comparative, not absolute.
	Similarity float32
}

// Search finds the passages that best answer a query.
//
// Hybrid, and the two halves fail differently on purpose. Vectors find a passage that means the same
// thing in other words, and miss an exact identifier they never saw during training — a serial
// number, an internal codename, a flag. Words find exactly that, and miss every paraphrase. Running
// one of them alone means accepting its blind spot as a property of the product.
//
// The two are fused by reciprocal rank rather than by adding scores, because their scores share no
// scale. What survives is agreement: a passage both rankers put near the top beats one that either
// loved alone.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("knowledge: an empty query")
	}
	if limit <= 0 {
		limit = 5
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ix, err := s.load()
	if err != nil {
		return nil, err
	}
	chunks := ix.Chunks()
	if len(chunks) == 0 {
		return nil, nil // an empty corpus is an empty answer, not a failure
	}

	// The query is embedded before anything else, so a provider that is down fails the search rather
	// than quietly degrading it to keyword-only — which would look like bad retrieval, not an outage.
	vecs, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("knowledge: embedding the query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("knowledge: embedding the query returned %d vectors", len(vecs))
	}
	qv := normalize(vecs[0])

	sims, err := similarities(chunks, qv)
	if err != nil {
		return nil, err
	}
	semantic := top(slices.Clone(sims), candidates)
	lexical := rankLexical(chunks, query)

	fused := fuse(chunks, semantic, lexical, sims)
	if len(fused) > limit {
		fused = fused[:limit]
	}
	if s.scanner != nil {
		// The same treatment memory_read gives its own notes: a document can hold a value from the
		// vault because somebody pasted it there, and a search result is how that value would reach
		// the conversation.
		for i := range fused {
			fused[i].Text = string(s.scanner.RedactIngress([]byte(fused[i].Text)))
		}
	}
	return fused, nil
}

// scored is one chunk's position in one ranker.
type scored struct {
	idx   int
	score float64
}

// similarities is the cosine between the query and every passage, in index order.
//
// Computed for all of them rather than only the shortlist, because the number is reported per
// result and a passage that reached the answer through the LEXICAL ranker still has to be able to
// say how close it is semantically — often the most informative pair of numbers in the set: an
// exact identifier match with a low cosine is precisely what the hybrid exists to find.
func similarities(chunks []StoredChunk, qv []float32) ([]scored, error) {
	out := make([]scored, len(chunks))
	for i, c := range chunks {
		v, err := decodeVector(c.Vector)
		if err != nil {
			return nil, fmt.Errorf("knowledge: %s: %w", c.Path, err)
		}
		out[i] = scored{idx: i, score: float64(dot(qv, v))}
	}
	return out, nil
}

// rankLexical orders chunks by BM25 over the query's terms.
//
// The half that finds an exact string. A vector for "NOCTURN_EMBED_DIMS" is a vector for whatever
// the model made of an unfamiliar token; BM25 simply matches it, and matches it harder for being
// rare across the corpus.
func rankLexical(chunks []StoredChunk, query string) []scored {
	terms := tokenize(query)
	if len(terms) == 0 {
		return nil
	}

	docs := make([][]string, len(chunks))
	df := map[string]int{}
	total := 0
	for i, c := range chunks {
		// The heading is part of the searchable text: a passage under "Database" should match a query
		// about the database even when the word appears nowhere in the body.
		docs[i] = tokenize(c.Heading + " " + c.Text)
		total += len(docs[i])
		for t := range uniq(docs[i]) {
			df[t]++
		}
	}
	avgLen := float64(total) / float64(len(chunks))

	out := make([]scored, 0, len(chunks))
	for i, doc := range docs {
		tf := map[string]int{}
		for _, t := range doc {
			tf[t]++
		}
		var score float64
		for _, q := range terms {
			f := float64(tf[q])
			if f == 0 {
				continue
			}
			// Robertson/Sparck Jones IDF with the +0.5 smoothing, so a term in every document
			// contributes about nothing instead of going negative.
			idf := math.Log(1 + (float64(len(chunks))-float64(df[q])+0.5)/(float64(df[q])+0.5))
			norm := f + bm25K1*(1-bm25B+bm25B*float64(len(doc))/avgLen)
			score += idf * (f * (bm25K1 + 1)) / norm
		}
		if score > 0 {
			out = append(out, scored{idx: i, score: score})
		}
	}
	return top(out, candidates)
}

// top sorts descending by score and keeps at most n. Ties break on index so two identical queries
// return the same order — a result list that reshuffles reads as instability that is not there.
func top(s []scored, n int) []scored {
	slices.SortFunc(s, func(a, b scored) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.idx, b.idx))
	})
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// fuse combines the two rankings by reciprocal rank.
//
// Only positions are used. A passage at rank 1 contributes 1/(60+1) whichever ranker put it there,
// so neither half can dominate by having larger numbers, and a passage both rankers placed well
// outranks one that a single ranker placed first.
func fuse(chunks []StoredChunk, semantic, lexical, sims []scored) []Result {
	type acc struct {
		score            float64
		semRank, lexRank int
	}
	byIdx := map[int]*acc{}
	at := func(idx int) *acc {
		if a := byIdx[idx]; a != nil {
			return a
		}
		a := &acc{}
		byIdx[idx] = a
		return a
	}

	for rank, s := range semantic {
		a := at(s.idx)
		a.semRank = rank + 1
		a.score += 1 / float64(rrfK+rank+1)
	}
	for rank, s := range lexical {
		a := at(s.idx)
		a.lexRank = rank + 1
		a.score += 1 / float64(rrfK+rank+1)
	}

	out := make([]Result, 0, len(byIdx))
	for idx, a := range byIdx {
		out = append(out, Result{
			Chunk:      chunks[idx].Chunk,
			Score:      a.score,
			Semantic:   a.semRank,
			Lexical:    a.lexRank,
			Similarity: float32(sims[idx].score),
		})
	}
	slices.SortFunc(out, func(x, y Result) int {
		return cmp.Or(
			cmp.Compare(y.Score, x.Score),
			cmp.Compare(x.Path, y.Path),
			cmp.Compare(x.Offset, y.Offset),
		)
	})
	return out
}

// tokenize lowercases and splits on anything that is not a letter or digit.
//
// Underscores and dots are separators too, which is deliberate: somebody searching for
// "NOCTURN_EMBED_DIMS" should also find a passage that only says "embed dims", and the whole term
// still matches as its parts.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// uniq is the set of terms in a document, for document frequency.
func uniq(terms []string) map[string]struct{} {
	out := make(map[string]struct{}, len(terms))
	for _, t := range terms {
		out[t] = struct{}{}
	}
	return out
}
