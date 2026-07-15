package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// The observer tracks a forest of calls by id: concurrent roots run at once, a
// nested effect folds into its parent's subtree when it ends, and a root commits
// to the transcript (carrying its children) only when it itself ends.
func TestHandleToolEvent_ConcurrentRootsAndNesting(t *testing.T) {
	m := chatModel{width: 80}
	ev := func(id, parent uint64, phase tool.Phase, name string) tool.Event {
		return tool.Event{ID: id, Parent: parent, Tool: name, Phase: phase}
	}

	m.handleToolEvent(ev(1, 0, tool.Start, "http.read"))
	m.handleToolEvent(ev(2, 0, tool.Start, "code.run"))
	m.handleToolEvent(ev(3, 2, tool.Start, "dns.resolve"))
	if len(m.active) != 3 || len(m.roots) != 2 {
		t.Fatalf("active=%d roots=%d, want 3 active / 2 roots", len(m.active), len(m.roots))
	}

	// The nested effect ends first: it folds into its parent's subtree, uncommitted.
	m.handleToolEvent(ev(3, 2, tool.End, "dns.resolve"))
	if len(m.entries) != 0 {
		t.Fatalf("nested effect committed before its parent ended: %d entries", len(m.entries))
	}
	if p := m.active[2]; p == nil || len(p.children) != 1 || p.children[0].name != "dns.resolve" {
		t.Fatalf("nested effect not folded into parent code.run: %+v", p)
	}

	// Roots end → each commits as a toolEntry (http.read first, then code.run).
	m.handleToolEvent(ev(1, 0, tool.End, "http.read"))
	m.handleToolEvent(ev(2, 0, tool.End, "code.run"))
	if len(m.entries) != 2 || len(m.active) != 0 || len(m.roots) != 0 {
		t.Fatalf("entries=%d active=%d roots=%d, want 2/0/0", len(m.entries), len(m.active), len(m.roots))
	}
	out := m.entries[1].render(&m, 80)
	if !strings.Contains(out, "code.run") || !strings.Contains(out, "dns.resolve") {
		t.Fatalf("committed code.run entry missing its nested effect:\n%s", out)
	}
}

// The transcript re-wraps at the current width: the same entry renders to more
// lines when narrow than when wide.
func TestUserEntry_RewrapsWithWidth(t *testing.T) {
	m := chatModel{}
	e := &userEntry{text: strings.TrimSpace(strings.Repeat("word ", 40))}
	narrow := lipgloss.Height(e.render(&m, 20))
	wide := lipgloss.Height(e.render(&m, 100))
	if narrow <= wide {
		t.Fatalf("narrow render (%d lines) should wrap to more lines than wide (%d)", narrow, wide)
	}
}

// Tool headlines are compact, not raw JSON.
func TestToolHeadline_Compact(t *testing.T) {
	cases := []struct{ name, args, want string }{
		{"http.read", `{"url":"https://example.com/x"}`, "http.read example.com/x"},
		{"dns.resolve", `{"host":"google.com"}`, "dns.resolve google.com"},
		{"code.run", `{"source":"1+1"}`, "code.run"},
	}
	for _, c := range cases {
		if got := toolHeadline(c.name, c.args); got != c.want {
			t.Fatalf("toolHeadline(%s) = %q, want %q", c.name, got, c.want)
		}
	}
}

// The approval prompt reserves exactly the rows it renders (guards the layout
// off-by-one that used to clip the transcript during an approval).
func TestApprovalHeight_MatchesRender(t *testing.T) {
	m := chatModel{width: 40, approval: &approvalPrompt{
		intent:  "POST api.example.com",
		options: []hitl.Option{{Label: "Allow once"}, {Label: "Allow session"}, {Label: "Deny"}},
	}}
	if got := lipgloss.Height(m.approvalView()); got != m.approvalHeight() {
		t.Fatalf("approvalView renders %d lines, layout reserves %d", got, m.approvalHeight())
	}
}
