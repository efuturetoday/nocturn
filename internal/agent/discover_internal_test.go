package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// loadAgent leaves the name empty when the frontmatter omits it — Discover resolves
// the name (defaulting to the folder), so a nameless agent.md is no longer an error.
func TestLoadAgent_MissingName_LeftForDiscover(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "agent.md")
	if err := os.WriteFile(path, []byte("---\ntools:\n  - http\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := loadAgent(path)
	if err != nil {
		t.Fatalf("loadAgent = %v, want no error (name defaulting is Discover's job)", err)
	}
	if a == nil || a.Name != "" {
		t.Fatalf("loadAgent name = %+v, want empty (resolved by Discover)", a)
	}
}

func TestLoadAgent_AbsentFile_NilNil(t *testing.T) {
	t.Parallel()

	a, err := loadAgent(filepath.Join(t.TempDir(), "nope", "agent.md"))
	if err != nil {
		t.Errorf("loadAgent on absent file returned error: %v", err)
	}
	if a != nil {
		t.Errorf("loadAgent on absent file = %+v, want nil", a)
	}
}

func TestSplitFrontmatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		in       string
		wantHead string
		wantBody string
	}{
		{
			name:     "no leading dashes -> all body",
			in:       "hello\nworld",
			wantHead: "",
			wantBody: "hello\nworld",
		},
		{
			name:     "leading whitespace then no dashes -> trimmed body",
			in:       "  \n\nhello there",
			wantHead: "",
			wantBody: "hello there",
		},
		{
			name:     "unterminated head -> all head",
			in:       "---\nname: x\nno closing fence",
			wantHead: "\nname: x\nno closing fence",
			wantBody: "",
		},
		{
			name:     "well formed",
			in:       "---\nname: x\n---\nbody",
			wantHead: "\nname: x",
			wantBody: "body",
		},
		{
			name:     "well formed multi-line body",
			in:       "---\na: 1\n---\nline1\nline2\n",
			wantHead: "\na: 1",
			wantBody: "line1\nline2\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			head, body := splitFrontmatter([]byte(tt.in))
			if string(head) != tt.wantHead {
				t.Errorf("head = %q, want %q", head, tt.wantHead)
			}
			if string(body) != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}
