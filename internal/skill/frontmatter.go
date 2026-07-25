package skill

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// maxFrontmatterBytes caps the YAML block BEFORE parsing — a bomb guard (alias expansion / oversized
// input) so a malformed or hostile skill file cannot make the parser do unbounded work. Frontmatter
// is a handful of scalar fields; 16 KB is generous.
const maxFrontmatterBytes = 16 << 10

// meta is the typed target for the frontmatter. Decoding into a struct (not a free-form map) keeps
// YAML's type coercion contained. Only the spec fields we use are recognized.
type meta struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// parseFrontmatter splits a SKILL.md into its frontmatter fields and Markdown body. The file must
// open with a `---` line; everything up to the next `---` line is the YAML frontmatter (size-capped,
// then decoded into a typed struct), and everything after is the body. A missing frontmatter block
// is an error (the caller skips the skill); a YAML syntax error is likewise returned.
func parseFrontmatter(src []byte) (meta, string, error) {
	rest, ok := bytes.CutPrefix(src, []byte("---\n"))
	if !ok {
		// Tolerate a leading BOM / CRLF opener before giving up.
		if trimmed := bytes.TrimLeft(src, "\ufeff\r\n \t"); bytes.HasPrefix(trimmed, []byte("---")) {
			if r, ok2 := cutFirstLine(trimmed); ok2 {
				rest = r
			} else {
				return meta{}, "", errNoFrontmatter
			}
		} else {
			return meta{}, "", errNoFrontmatter
		}
	}

	// Find the closing delimiter: a line that is exactly "---" (or "..." per YAML).
	fm, body, ok := splitAtCloser(rest)
	if !ok {
		return meta{}, "", errNoFrontmatter
	}
	if len(fm) > maxFrontmatterBytes {
		return meta{}, "", fmt.Errorf("skill: frontmatter exceeds %d bytes", maxFrontmatterBytes)
	}

	var m meta
	if err := yaml.Unmarshal(fm, &m); err != nil {
		return meta{}, "", fmt.Errorf("skill: invalid frontmatter YAML: %w", err)
	}
	return m, string(body), nil
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
