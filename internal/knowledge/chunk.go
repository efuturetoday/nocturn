package knowledge

import (
	"strings"
	"unicode/utf8"
)

const (
	// targetBytes is what a chunk aims for. Retrieval wants a passage large enough to answer
	// something on its own and small enough that most of it is about one thing — a whole document is
	// a vector describing an average of its topics, which matches every query weakly and none well.
	targetBytes = 2000

	// minBytes is the shortest chunk worth standing alone. A trailing paragraph of two lines is
	// appended to the previous chunk instead: on its own it embeds mostly noise and, being short,
	// scores misleadingly high against a short query.
	minBytes = 300

	// overlapBytes is carried from the end of one chunk into the start of the next, so a passage that
	// happens to straddle a boundary is still whole in one of them. Without it, the sentence that
	// answers the question is the sentence that got cut in half.
	overlapBytes = 200

	// maxHeadingPath bounds the breadcrumb prefixed to a chunk before embedding, so a deeply nested
	// document does not spend its whole vector on its own table of contents.
	maxHeadingPath = 120
)

// Chunk is one indexed passage: where it came from, and what it says.
type Chunk struct {
	// Path is workspace-relative and forward-slashed — the same string a file tool would take.
	Path string `json:"path"`
	// Heading is the breadcrumb of Markdown headings above the passage ("Setup > Database"), empty
	// for text before any heading. It is shown in a result so a reader can place the quote, and it is
	// embedded with the text so a passage saying "set it to 30" is not orphaned from what "it" is.
	Heading string `json:"heading"`
	// Offset is the byte offset of Text within the file, so a result can point at a place rather than
	// at a document.
	Offset int `json:"offset"`
	// Text is the passage itself, as it appears in the file.
	Text string `json:"text"`
}

// embedText is what actually gets embedded: the location, then the passage.
//
// The breadcrumb is not decoration. Documents are written assuming their headings, so a passage
// reading "leave this unset in production" means nothing without the section it sits under, and a
// vector built from the passage alone cannot recover it.
func (c Chunk) embedText() string {
	loc := c.Path
	if c.Heading != "" {
		loc += " > " + c.Heading
	}
	if len(loc) > maxHeadingPath {
		loc = loc[:maxHeadingPath]
	}
	return loc + "\n\n" + c.Text
}

// chunkSections packs a Reader's sections into passages. Format-independent by construction: by the
// time it runs, whatever the file was has already become sections.
//
// A section is never crossed — it is where the document itself said the subject changed, and a
// passage spanning two of them answers neither. Within one, paragraphs accumulate until the target
// is reached. A section longer than the target is split at paragraph boundaries; a paragraph longer
// than the target is split on lines; a single line longer than the target is cut on a rune boundary
// — each fallback reached only when the gentler one cannot apply.
func chunkSections(path string, secs []Section) []Chunk {
	var out []Chunk
	for _, sec := range secs {
		out = append(out, sec.chunks(path)...)
	}

	// A too-short final chunk is folded back rather than left to embed mostly noise. Only within one
	// section: merging across a heading would rebuild exactly the boundary the split exists for.
	for i := len(out) - 1; i > 0; i-- {
		if len(out[i].Text) >= minBytes || out[i].Heading != out[i-1].Heading {
			continue
		}
		out[i-1].Text = out[i-1].Text + "\n\n" + out[i].Text
		out = append(out[:i], out[i+1:]...)
	}
	return out
}

// Section is a titled span of a document, as a Reader found it.
//
// This is the seam between "what this file format looks like" and "how a passage is sized". A
// Markdown reader cuts at headings, a PDF reader would cut at pages or outline entries, a real
// CommonMark parser would cut at the same headings but understand fenced blocks and tables — and
// none of that changes how the sections are then packed into passages.
type Section struct {
	// Heading is the breadcrumb above the body ("Setup > Database"), empty where the format has no
	// such notion or the span sits above the first one.
	Heading string
	// Body is the text under it.
	Body string
	// Offset is where Body starts in the file, so a result can point at a place.
	Offset int
}

// Reader turns one document format into sections. It is a port: the package chunks and embeds, and
// knows nothing about how a byte slice became text.
//
// Deliberately narrow. Extracting from a PDF or an Office file needs a dependency that has no
// business inside a package the whole workspace links, so it belongs behind this interface in a
// package of its own — the same reason the embedding client is its own package.
type Reader interface {
	// Handles reports whether this reader takes that file extension, lowercased and with its dot.
	Handles(ext string) bool
	// Sections extracts the document. A reader for a binary format is what turns bytes into text.
	Sections(data []byte) ([]Section, error)
}

// MarkdownReader splits Markdown and plain text at ATX headings.
//
// Hand-written rather than a CommonMark parser, and that is a scope decision rather than a
// preference: what chunking needs from Markdown is where the subject changes, which ATX headings
// answer, and a parser would add a dependency for the ninety percent of syntax that does not affect
// a boundary. When fenced blocks containing "#" become a real problem, this is the thing that gets
// replaced — behind the port, without the rest of the package noticing.
type MarkdownReader struct{}

var _ Reader = MarkdownReader{}

// Handles takes Markdown and plain text. Anything else needs its own reader.
func (MarkdownReader) Handles(ext string) bool {
	switch ext {
	case ".md", ".markdown", ".mdx", ".txt", ".text":
		return true
	}
	return false
}

// Sections splits the document at headings. It never fails: text is text.
func (MarkdownReader) Sections(data []byte) ([]Section, error) {
	return markdownSections(string(data)), nil
}

// markdownSections splits a document at ATX headings, carrying the breadcrumb down the tree.
func markdownSections(content string) []Section {
	var (
		out   []Section
		trail []string // one entry per heading level currently open
		body  strings.Builder
		start int
		pos   int
	)
	flush := func() {
		if strings.TrimSpace(body.String()) != "" {
			out = append(out, Section{Heading: strings.Join(trail, " > "), Body: body.String(), Offset: start})
		}
		body.Reset()
	}

	for line := range strings.SplitSeq(content, "\n") {
		lineLen := len(line) + 1 // the newline SplitSeq removed
		if level, title := heading(line); level > 0 {
			flush()
			// Trim the trail to this level, then push. A level jumping from 1 to 3 simply pads, so a
			// document with skipped levels still produces a sensible breadcrumb.
			if level-1 < len(trail) {
				trail = trail[:level-1]
			}
			for len(trail) < level-1 {
				trail = append(trail, "")
			}
			trail = append(trail, title)
			pos += lineLen
			start = pos
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
		pos += lineLen
	}
	flush()
	return out
}

// heading reports the ATX level and title of a Markdown heading line, or 0.
func heading(line string) (int, string) {
	t := strings.TrimLeft(line, " ")
	level := len(t) - len(strings.TrimLeft(t, "#"))
	if level == 0 || level > 6 {
		return 0, ""
	}
	rest := t[level:]
	if rest != "" && !strings.HasPrefix(rest, " ") {
		return 0, "" // "#hashtag" is not a heading
	}
	return level, strings.TrimSpace(strings.Trim(rest, "#"))
}

// chunks splits one section's body into passages at paragraph boundaries.
func (s Section) chunks(path string) []Chunk {
	var (
		out []Chunk
		cur strings.Builder
		at  = s.Offset
		pos = s.Offset
	)
	emit := func() {
		text := strings.TrimSpace(cur.String())
		if text == "" {
			cur.Reset()
			return
		}
		out = append(out, Chunk{Path: path, Heading: s.Heading, Offset: at, Text: text})
		cur.Reset()
		// Carry the tail forward so a passage straddling the boundary survives whole somewhere.
		tail := overlap(text)
		cur.WriteString(tail)
		at = pos - len(tail)
	}

	for _, para := range paragraphs(s.Body) {
		for _, piece := range split(para.text) {
			if cur.Len() > 0 && cur.Len()+len(piece) > targetBytes {
				emit()
			}
			if cur.Len() > 0 {
				cur.WriteString("\n\n")
			}
			cur.WriteString(piece)
			pos = s.Offset + para.offset + len(para.text)
		}
	}
	emit()
	// The last emit left an overlap tail behind with nothing following it; it is already inside the
	// previous chunk, so it is not a chunk of its own.
	return out
}

// paragraph is a block of text and where it started.
type paragraph struct {
	text   string
	offset int
}

// paragraphs splits on blank lines, dropping empties.
func paragraphs(body string) []paragraph {
	var (
		out []paragraph
		cur strings.Builder
		at  int
		pos int
	)
	push := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, paragraph{text: t, offset: at})
		}
		cur.Reset()
	}
	for line := range strings.SplitSeq(body, "\n") {
		if strings.TrimSpace(line) == "" {
			push()
			pos += len(line) + 1
			at = pos
			continue
		}
		if cur.Len() == 0 {
			at = pos
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		pos += len(line) + 1
	}
	push()
	return out
}

// split breaks a single oversized paragraph down, on lines first and only then mid-line.
func split(para string) []string {
	if len(para) <= targetBytes {
		return []string{para}
	}
	var out []string
	var cur strings.Builder
	for line := range strings.SplitSeq(para, "\n") {
		for len(line) > targetBytes {
			// A single line longer than the target: a minified file, a base64 blob, one very long
			// sentence. Cut on a rune boundary so the text stays valid UTF-8.
			cut := cutPoint(line, targetBytes)
			out = append(out, line[:cut])
			line = line[cut:]
		}
		if cur.Len() > 0 && cur.Len()+len(line) > targetBytes {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	if t := strings.TrimSpace(cur.String()); t != "" {
		out = append(out, t)
	}
	return out
}

// cutPoint is the largest index at or below n that does not split a rune.
func cutPoint(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	if n == 0 {
		return len(s) // one rune longer than the whole budget: take it rather than loop forever
	}
	return n
}

// overlap returns the tail of text to repeat in the next chunk, cut at a paragraph or sentence
// boundary where one is available so the repeat reads as text rather than as a fragment.
func overlap(text string) string {
	if len(text) <= overlapBytes {
		return text
	}
	tail := text[cutPoint(text, len(text)-overlapBytes):]
	if i := strings.Index(tail, "\n\n"); i >= 0 && i < len(tail)-1 {
		return strings.TrimSpace(tail[i+2:])
	}
	if i := strings.Index(tail, ". "); i >= 0 && i < len(tail)-2 {
		return strings.TrimSpace(tail[i+2:])
	}
	return strings.TrimSpace(tail)
}
