// Package frontmatter parses the YAML preamble a Markdown file may open with — the shape the Agent
// Skills standard uses, and the same one nocturn's memory notes use to describe themselves. Kept as
// its own tiny package because three consumers parsing "---" blocks three ways would drift.
package frontmatter

import (
	"bytes"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// ErrNone is returned when a file has no --- delimited frontmatter block.
var ErrNone = errors.New("frontmatter: none found (expected a leading --- block)")

// maxBytes caps the YAML block BEFORE parsing — a bomb guard (alias expansion / oversized input) so
// a malformed or hostile file cannot make the parser do unbounded work. Frontmatter is a handful of
// scalar fields; 16 KB is generous.
const maxBytes = 16 << 10

// Meta is the typed target for the frontmatter. Decoding into a struct (not a free-form map) keeps
// YAML's type coercion contained. Only the fields nocturn uses are recognized.
type Meta struct {
	Name        string `yaml:"name,omitempty"`
	Description string `yaml:"description,omitempty"`
}

// Parse splits src into its frontmatter fields and Markdown body. The file must open with a `---`
// line; everything up to the next `---` line is the YAML frontmatter (size-capped, then decoded into
// a typed struct), and everything after is the body. A missing block returns ErrNone (the caller
// decides whether that is fatal); a YAML syntax error is likewise returned.
func Parse(src []byte) (Meta, string, error) {
	rest, ok := bytes.CutPrefix(src, []byte("---\n"))
	if !ok {
		// Tolerate a leading BOM / CRLF opener before giving up.
		if trimmed := bytes.TrimLeft(src, "\ufeff\r\n \t"); bytes.HasPrefix(trimmed, []byte("---")) {
			if r, ok2 := cutFirstLine(trimmed); ok2 {
				rest = r
			} else {
				return Meta{}, "", ErrNone
			}
		} else {
			return Meta{}, "", ErrNone
		}
	}

	// Find the closing delimiter: a line that is exactly "---" (or "..." per YAML).
	fm, body, ok := splitAtCloser(rest)
	if !ok {
		return Meta{}, "", ErrNone
	}
	if len(fm) > maxBytes {
		return Meta{}, "", fmt.Errorf("frontmatter: exceeds %d bytes", maxBytes)
	}

	var m Meta
	if err := yaml.Unmarshal(fm, &m); err != nil {
		return Meta{}, "", fmt.Errorf("frontmatter: invalid YAML: %w", err)
	}
	return m, string(body), nil
}

// Render is Parse's inverse: it emits a `---` delimited block for m followed by body. Producing the
// YAML through the marshaller rather than by string concatenation is the point — a description
// holding a colon, a quote or a newline would otherwise silently corrupt the block, and the caller
// would have to know YAML's quoting rules to avoid it.
func Render(m Meta, body string) string {
	y, err := yaml.Marshal(m)
	if err != nil {
		// Meta is two strings; Marshal cannot fail on it. Degrade to a bare body rather than lose the
		// note, and let the summary fall back to its first line.
		return body
	}
	return "---\n" + string(y) + "---\n" + body
}

// cutFirstLine drops the first line (the opening delimiter) and returns the rest.
func cutFirstLine(b []byte) ([]byte, bool) {
	if _, after, ok := bytes.Cut(b, []byte{'\n'}); ok {
		return after, true
	}
	return nil, false
}

// splitAtCloser returns the frontmatter bytes before the closing delimiter line and the body after
// it. The closer is a line equal to "---" or "...".
func splitAtCloser(rest []byte) (fm, body []byte, ok bool) {
	start := 0
	for start <= len(rest) {
		nl := bytes.IndexByte(rest[start:], '\n')
		var line []byte
		if nl < 0 {
			line = rest[start:]
		} else {
			line = rest[start : start+nl]
		}
		trimmed := bytes.TrimRight(line, "\r")
		if bytes.Equal(trimmed, []byte("---")) || bytes.Equal(trimmed, []byte("...")) {
			fm = rest[:start]
			if nl < 0 {
				return fm, nil, true
			}
			return fm, rest[start+nl+1:], true
		}
		if nl < 0 {
			break
		}
		start += nl + 1
	}
	return nil, nil, false
}
