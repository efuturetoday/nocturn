package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAgent_MissingName_Error(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "agent.md")
	if err := os.WriteFile(path, []byte("---\ntools:\n  - http\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := loadAgent(path)
	if err == nil {
		t.Fatalf("loadAgent = (%+v, nil), want missing-name error", a)
	}
	if !strings.Contains(err.Error(), "missing name") {
		t.Errorf("error = %q, want it to mention missing name", err)
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
