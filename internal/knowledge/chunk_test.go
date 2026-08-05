package knowledge

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// chunkMarkdown runs the Markdown reader and the format-independent chunker together, which is the
// pair every test here is really about.
func chunkMarkdown(t *testing.T, path, content string) []Chunk {
	t.Helper()
	secs, err := MarkdownReader{}.Sections([]byte(content))
	if err != nil {
		t.Fatalf("Sections: %v", err)
	}
	return chunkSections(path, secs)
}

// para builds a paragraph of roughly n bytes out of distinguishable words.
func para(word string, n int) string {
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(word)
		b.WriteByte(' ')
	}
	return strings.TrimSpace(b.String())
}

// A heading is where a document itself says the subject changed. Cutting across one produces a
// passage about two things that answers neither.
func TestChunk_NeverCrossesAHeading(t *testing.T) {
	doc := "# Alpha\n\nabout alpha\n\n# Beta\n\nabout beta\n"
	chunks := chunkMarkdown(t, "notes.md", doc)

	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want one per heading: %+v", len(chunks), chunks)
	}
	for _, c := range chunks {
		if strings.Contains(c.Text, "alpha") && strings.Contains(c.Text, "beta") {
			t.Errorf("a chunk spans both sections: %q", c.Text)
		}
	}
	if chunks[0].Heading != "Alpha" || chunks[1].Heading != "Beta" {
		t.Errorf("headings = %q, %q", chunks[0].Heading, chunks[1].Heading)
	}
}

// The breadcrumb is what makes a passage readable out of context: "leave this unset" means nothing
// without the section it sits under, and a vector built from the passage alone cannot recover it.
func TestChunk_HeadingBreadcrumbNests(t *testing.T) {
	doc := "# Setup\n\nintro text here\n\n## Database\n\nset the pool to 30\n\n### Tuning\n\ndeeper still\n"
	chunks := chunkMarkdown(t, "guide.md", doc)

	want := []string{"Setup", "Setup > Database", "Setup > Database > Tuning"}
	if len(chunks) != len(want) {
		t.Fatalf("got %d chunks, want %d", len(chunks), len(want))
	}
	for i, w := range want {
		if chunks[i].Heading != w {
			t.Errorf("chunk %d heading = %q, want %q", i, chunks[i].Heading, w)
		}
	}
	// What is embedded carries the location, not just the text.
	if got := chunks[1].embedText(); !strings.HasPrefix(got, "guide.md > Setup > Database\n\n") {
		t.Errorf("embedText = %q, want the breadcrumb first", got)
	}
}

// Text before any heading still belongs to the index.
func TestChunk_PreambleHasNoHeading(t *testing.T) {
	chunks := chunkMarkdown(t, "notes.md", "some loose text\n\n# Later\n\nmore\n")
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}
	if chunks[0].Heading != "" {
		t.Errorf("preamble heading = %q, want empty", chunks[0].Heading)
	}
	if !strings.Contains(chunks[0].Text, "loose text") {
		t.Errorf("preamble lost: %q", chunks[0].Text)
	}
}

func TestChunk_LongSectionSplitsAtParagraphs(t *testing.T) {
	doc := "# Long\n\n" + para("alpha", 1200) + "\n\n" + para("beta", 1200) + "\n\n" + para("gamma", 1200) + "\n"
	chunks := chunkMarkdown(t, "long.md", doc)

	if len(chunks) < 2 {
		t.Fatalf("a %d-byte section produced %d chunks", len(doc), len(chunks))
	}
	for i, c := range chunks {
		if c.Heading != "Long" {
			t.Errorf("chunk %d heading = %q", i, c.Heading)
		}
		// The target is a goal, not a hard cap — a chunk is allowed to overshoot by the piece that
		// crossed it — but nothing should be wildly over.
		if len(c.Text) > 2*targetBytes {
			t.Errorf("chunk %d is %d bytes, far past the %d target", i, len(c.Text), targetBytes)
		}
	}
}

// Without overlap, the sentence that answers the question is the sentence that got cut in half.
func TestChunk_ConsecutiveChunksOverlap(t *testing.T) {
	doc := "# Long\n\n" + para("alpha", 1500) + "\n\n" + para("beta", 1500) + "\n"
	chunks := chunkMarkdown(t, "long.md", doc)
	if len(chunks) < 2 {
		t.Skipf("this document produced %d chunk(s); nothing to overlap", len(chunks))
	}

	prev, next := chunks[0].Text, chunks[1].Text
	// The head of the next chunk has to appear at the end of the previous one. A substring rather
	// than a word check on purpose: with no sentence or paragraph boundary to cut on, the carried
	// tail is allowed to start mid-word — it only has to be the same text.
	// The carried tail is the longest prefix of the next chunk that the previous one ends with. A
	// substring rather than a word check on purpose: with no sentence or paragraph boundary to cut
	// on, the tail is allowed to start mid-word — it only has to be the same text, in the same place.
	carried := 0
	for k := min(len(prev), len(next)); k > 0; k-- {
		if strings.HasSuffix(prev, next[:k]) {
			carried = k
			break
		}
	}
	if carried < overlapBytes/2 {
		t.Errorf("the previous chunk ends with only %d bytes of the next one, want about %d:\n prev tail: %q\n next head: %q",
			carried, overlapBytes, prev[max(0, len(prev)-80):], next[:min(80, len(next))])
	}
}

// A short trailing paragraph on its own embeds mostly noise and, being short, scores misleadingly
// high against a short query. It belongs to the passage before it.
func TestChunk_ShortTailIsFoldedBack(t *testing.T) {
	doc := "# S\n\n" + para("alpha", 1900) + "\n\nok.\n"
	chunks := chunkMarkdown(t, "t.md", doc)
	for i, c := range chunks {
		if len(c.Text) < minBytes && len(chunks) > 1 {
			t.Errorf("chunk %d stands alone at %d bytes: %q", i, len(c.Text), c.Text)
		}
	}
	joined := strings.Join(texts(chunks), " ")
	if !strings.Contains(joined, "ok.") {
		t.Error("the short tail was dropped instead of folded back")
	}
}

// A minified file, a base64 blob, one very long sentence: there is no paragraph or line to cut on,
// and the result still has to be valid UTF-8.
func TestChunk_OneEnormousLineIsCutOnRuneBoundaries(t *testing.T) {
	long := strings.Repeat("äöü", 3000) // multi-byte runes, no spaces, no newlines
	chunks := chunkMarkdown(t, "blob.md", "# B\n\n"+long+"\n")

	if len(chunks) < 2 {
		t.Fatalf("a %d-byte line produced %d chunk(s)", len(long), len(chunks))
	}
	for i, c := range chunks {
		if !utf8.ValidString(c.Text) {
			t.Errorf("chunk %d is not valid UTF-8", i)
		}
	}
}

// "#hashtag" is not a heading, and neither is a seventh-level run of hashes.
func TestHeading_OnlyRealATXHeadings(t *testing.T) {
	cases := map[string]struct {
		level int
		title string
	}{
		"# Title":        {1, "Title"},
		"### Deep":       {3, "Deep"},
		"  ## Indented":  {2, "Indented"},
		"## Closed ##":   {2, "Closed"},
		"#hashtag":       {0, ""},
		"####### Seven":  {0, ""},
		"not a heading":  {0, ""},
		"":               {0, ""},
		"#":              {1, ""},
		"a # mid-string": {0, ""},
	}
	for line, want := range cases {
		t.Run(line, func(t *testing.T) {
			level, title := heading(line)
			if level != want.level || title != want.title {
				t.Errorf("heading(%q) = %d, %q; want %d, %q", line, level, title, want.level, want.title)
			}
		})
	}
}

// An offset that does not point into the file is worse than none: a result would quote a place that
// is not where the text is.
func TestChunk_OffsetPointsIntoTheDocument(t *testing.T) {
	doc := "# Alpha\n\nfirst body\n\n# Beta\n\nsecond body\n"
	for _, c := range chunkMarkdown(t, "d.md", doc) {
		if c.Offset < 0 || c.Offset > len(doc) {
			t.Fatalf("offset %d is outside a %d-byte document", c.Offset, len(doc))
		}
		head := strings.Fields(c.Text)[0]
		if !strings.Contains(doc[c.Offset:], head) {
			t.Errorf("chunk at offset %d does not start anywhere at or after it (looking for %q)", c.Offset, head)
		}
	}
}

func TestChunk_EmptyDocumentProducesNothing(t *testing.T) {
	for _, doc := range []string{"", "   \n\n  \n", "# Heading only\n"} {
		if got := chunkMarkdown(t, "e.md", doc); len(got) != 0 {
			t.Errorf("chunking %q gave %d chunks, want none", doc, len(got))
		}
	}
}

func texts(cs []Chunk) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Text
	}
	return out
}

// What the default reader takes, and what it deliberately leaves for a reader that does not exist
// yet. A format nobody can extract must be refused rather than indexed as bytes: embedding a PDF's
// raw stream produces a vector for its compression, not for what it says.
func TestMarkdownReader_Handles(t *testing.T) {
	r := MarkdownReader{}
	for _, ext := range []string{".md", ".markdown", ".mdx", ".txt", ".text"} {
		if !r.Handles(ext) {
			t.Errorf("Handles(%q) = false, want true", ext)
		}
	}
	for _, ext := range []string{".pdf", ".docx", ".png", ".jpg", ".go", ".json", ".zip", "", ".MD"} {
		if r.Handles(ext) {
			t.Errorf("Handles(%q) = true — that needs a reader of its own", ext)
		}
	}
}

// A reader that finds no headings still produces the document, or plain text would vanish.
func TestMarkdownReader_PlainTextIsOneSection(t *testing.T) {
	secs, err := MarkdownReader{}.Sections([]byte("just some prose\n\nand a second paragraph\n"))
	if err != nil {
		t.Fatalf("Sections: %v", err)
	}
	if len(secs) != 1 {
		t.Fatalf("got %d sections, want 1", len(secs))
	}
	if secs[0].Heading != "" {
		t.Errorf("heading = %q, want empty", secs[0].Heading)
	}
	if !strings.Contains(secs[0].Body, "second paragraph") {
		t.Error("the body lost text")
	}
}
