package tui

import (
	"fmt"
	"path"
	"sort"
	"strings"

	tui "github.com/grindlemire/go-tui"

	"github.com/efuturetoday/nocturn/internal/workspace"
)

// The workspace view answers one question, and the question is why the key gets pressed: WHAT CAN
// THIS ASSISTANT DO RIGHT NOW, AND IS ANY OF IT BROKEN.
//
// That is really two questions, and the second one is urgent. So the view is a BOARD: the verdict
// first — every problem, at full page width, with its reason wrapped and never cut — then seven
// sections in fixed places, each showing a COUNT and at most four characterising lines. Whole lists
// live one keystroke deeper, on a page of their own.
//
// What it replaces showed everything at once, and that was the fault rather than the virtue. Seven
// cards were dealt into three columns by a shortest-first bin-packer, so nothing sat in the same
// place twice — a view opened BECAUSE something is wrong had no geography to scan, only text to
// read. Every line went through one renderer, so a server that was down and the fortieth tool name
// had identical geometry. And the lists were printed whole: a knowledge base of three hundred files
// buried the six facts anyone came for.
//
// The trade is deliberate: three sections are now one keystroke deep instead of zero. A wall that
// contains the answer is worse than a summary that points at it.

// sectionID is both the identity of a section and the digit that opens it. sectionBoard is the board
// itself, which is why it is zero: no digit opens it, Escape returns to it.
type sectionID uint8

const (
	sectionBoard sectionID = iota
	sectionAgents
	sectionMCP
	sectionExtensions
	sectionTools
	sectionKnowledge
	sectionMemory
	sectionSecrets
	sectionCount
)

// Filterable reports whether this page offers `/`. The three unbounded lists do; the rest are short
// enough that a filter would be a key that never earns its place — and the hint line must not offer
// what the page does not have.
func (s sectionID) Filterable() bool {
	return s == sectionTools || s == sectionKnowledge || s == sectionMemory
}

func (s sectionID) Title() string {
	switch s {
	case sectionAgents:
		return "agents"
	case sectionMCP:
		return "mcp"
	case sectionExtensions:
		return "extensions"
	case sectionTools:
		return "tools"
	case sectionKnowledge:
		return "knowledge"
	case sectionMemory:
		return "memory"
	case sectionSecrets:
		return "secrets"
	default:
		return "workspace"
	}
}

// tone is what a line MEANS, which decides how it is drawn. Four weights instead of one is the
// whole difference between a board that is scanned and a board that is read.
type tone uint8

const (
	toneNormal tone = iota
	toneDim
	toneGood
	toneWarn
	toneBad
)

// boardLine is one body line under a section header: a name in a sized column, and the rest dim
// beside it. Two aligned columns, so the eye can run down either.
type boardLine struct {
	Name string
	Note string
	Tone tone
	// Bar is a pre-drawn gauge, used where a RATIO is the fact — memory against the prompt budget.
	// Drawn in Go rather than by a <progress> element because it sits inline with text.
	Bar string
}

// boardSection is one box on the board.
type boardSection struct {
	ID    sectionID
	Count string // the metric, on the right of the header
	Body  []boardLine
}

// problem is something that was configured and is not working. It is the only thing on the board
// that gets the full page width, because its reason is the one string the view exists to deliver.
type problem struct {
	Bad    bool // a failure, as opposed to an errand somebody can run
	Name   string
	State  string
	Where  string
	Reason []string // already wrapped; capped on the board, whole on the page
	Goto   sectionID
}

// pageBlock is one entry on a section's own page: a heading, its facts, and prose that is wrapped
// and never truncated.
type pageBlock struct {
	Icon  string
	Tone  tone
	Name  string
	State string
	Meta  string
	Body  []string
	Extra string
}

// gridCell is one item of a long flat list, laid two or three to a row.
type gridCell struct {
	Left  string
	Right string
}

// subPage is a section shown whole.
type subPage struct {
	Summary string
	Blocks  []pageBlock
	Grid    []gridCell
	// Columns is how many cells share a row. Names differ in length between pages, so this is per
	// page rather than a constant.
	Columns int
}

// problemsCap is how many wrapped lines a reason gets on the board. Three is about 330 characters,
// which covers every real transport error; what does not fit says where the rest is. The page has
// no cap at all.
const problemsCap = 3

// capReason bounds a reason on the BOARD and says where the rest of it is. The board has six other
// sections to show; a stack trace would take all of it. Nothing is lost — the section's own page
// wraps the same text with no cap at all, and the pointer names the digit that opens it.
func capReason(lines []string, at sectionID) []string {
	if len(lines) <= problemsCap {
		return lines
	}
	lines = lines[:problemsCap]
	lines[problemsCap-1] = fmt.Sprintf("%s… [%d] for the rest", lines[problemsCap-1], at)
	return lines
}

// boardBodyCap bounds every section's body, so the board's height does not depend on the data. This
// is what makes "the verdict is on the first screen" true at three files and at three thousand.
const boardBodyCap = 4

// problems is the verdict: everything configured that is not working, worst first. A locked vault
// comes before a server that is merely waiting to be authorised, because it is the reason the
// authorising cannot happen.
func (a *app) problems() []problem {
	// Nothing is built while the view is shut. Every frame draws this template, and a workspace that
	// is merely OPEN would otherwise walk its inventory sixty times a minute for a box nobody is
	// looking at.
	if !a.ready() || !a.inspectOpen.Get() {
		return nil
	}
	width := a.inspectWidth()
	var out []problem

	if a.ws.VaultLocked() {
		out = append(out, problem{
			Bad: true, Name: "vault", State: "locked", Goto: sectionSecrets,
			Reason: capReason(wrap("no master key, so no credential can be read and no MCP server "+
				"can be authorised", width), sectionSecrets),
		})
	}
	for _, s := range a.ws.Inventory().MCP {
		if s.State == workspace.MCPConnected {
			continue
		}
		out = append(out, problem{
			Bad: s.State == workspace.MCPFailed, Name: s.Name, State: string(s.State),
			Where: s.URL, Goto: sectionMCP, Reason: capReason(wrap(s.Note, width), sectionMCP),
		})
	}
	// A failure sorts above an errand, and within each the order is the order they were declared —
	// stable, so the list does not reshuffle under the reader between two ticks.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Bad && !out[j].Bad })
	return out
}

// board is the seven sections, always all seven and always in this order. Fixed slots are the point:
// an empty section still holds its place, so where a thing lives is learned once.
func (a *app) board() []boardSection {
	return []boardSection{
		a.sectionAgents(), a.sectionTools(),
		a.sectionMCP(), a.sectionKnowledge(),
		a.sectionExtensions(), a.sectionMemory(),
		a.sectionSecrets(),
	}
}

func (a *app) sectionAgents() boardSection {
	s := boardSection{ID: sectionAgents}
	if !a.ready() {
		return s
	}
	agents := a.ws.Agents()
	s.Count = fmt.Sprint(len(agents))
	for _, ag := range agents {
		if len(s.Body) == boardBodyCap {
			s.Body = append(s.Body, more(len(agents)-boardBodyCap, "agents"))
			break
		}
		when := ag.When
		if when == "" {
			when = "manual"
		}
		// Autonomy is the one field on this board that is a SECURITY fact: it decides whether an
		// unattended run may ask a human for permission or must refuse on its own. So it is coloured,
		// and the schedule beside it is not.
		t := toneDim
		if ag.Autonomy == "guarded" {
			t = toneWarn
		}
		s.Body = append(s.Body, boardLine{Name: ag.Name, Note: string(ag.Autonomy) + " · " + when, Tone: t})
	}
	return s
}

func (a *app) sectionMCP() boardSection {
	s := boardSection{ID: sectionMCP}
	if !a.ready() {
		return s
	}
	mcp := a.ws.Inventory().MCP
	if len(mcp) == 0 {
		s.Count = "none"
		s.Body = append(s.Body, boardLine{Name: "none declared", Note: "add an mcp/*.json", Tone: toneDim})
		return s
	}
	up := 0
	for _, m := range mcp {
		if m.State == workspace.MCPConnected {
			up++
		}
	}
	s.Count = fmt.Sprintf("%d of %d up", up, len(mcp))
	for _, m := range mcp {
		if len(s.Body) == boardBodyCap {
			s.Body = append(s.Body, more(len(mcp)-boardBodyCap, "servers"))
			break
		}
		line := boardLine{Name: m.Name, Note: string(m.State), Tone: toneDim}
		switch m.State {
		case workspace.MCPConnected:
			line.Note = fmt.Sprintf("connected · %d tools", m.Tools)
			line.Tone = toneGood
		case workspace.MCPFailed:
			line.Tone = toneBad
		default:
			line.Tone = toneWarn
		}
		s.Body = append(s.Body, line)
	}
	return s
}

func (a *app) sectionExtensions() boardSection {
	s := boardSection{ID: sectionExtensions}
	if !a.ready() {
		return s
	}
	inv := a.ws.Inventory()
	if len(inv.Plugins)+len(inv.Skills) == 0 {
		s.Count = "none"
		s.Body = append(s.Body, boardLine{Name: "none installed", Note: "see sdk/_template", Tone: toneDim})
		return s
	}
	// Counted apart, not summed. A plugin contributes TOOLS and a skill contributes INSTRUCTIONS —
	// different authority, different consequence, so "7 extensions" would be a number about nothing.
	s.Count = fmt.Sprintf("%d plugins · %d skills", len(inv.Plugins), len(inv.Skills))
	for _, p := range inv.Plugins {
		if len(s.Body) == boardBodyCap {
			break
		}
		s.Body = append(s.Body, boardLine{Name: p, Note: "plugin", Tone: toneDim})
	}
	for _, k := range inv.Skills {
		if len(s.Body) == boardBodyCap {
			break
		}
		s.Body = append(s.Body, boardLine{Name: k, Note: "skill", Tone: toneDim})
	}
	if rest := len(inv.Plugins) + len(inv.Skills) - len(s.Body); rest > 0 {
		s.Body = append(s.Body, more(rest, "extensions"))
	}
	return s
}

// sectionTools describes the toolset by SHAPE rather than by listing it. Forty-seven identifiers do
// not answer "can it write a file"; `file 7` does, in one line that stays true at any size.
func (a *app) sectionTools() boardSection {
	s := boardSection{ID: sectionTools}
	if !a.ready() {
		return s
	}
	inv := a.ws.Inventory()
	s.Count = fmt.Sprint(len(inv.Tools))

	fromMCP := 0
	for _, m := range inv.MCP {
		if m.State == workspace.MCPConnected {
			fromMCP += m.Tools
		}
	}
	rest := max(len(inv.Tools)-fromMCP, 0)
	s.Body = append(s.Body, boardLine{
		Name: fmt.Sprintf("%d from mcp", fromMCP),
		Note: fmt.Sprintf("%d built-in, plugins and skills", rest),
		Tone: toneDim,
	})
	for _, line := range familyLines(inv.Tools, boardBodyCap-1) {
		s.Body = append(s.Body, boardLine{Name: "", Note: line, Tone: toneDim})
	}
	return s
}

func (a *app) sectionKnowledge() boardSection {
	s := boardSection{ID: sectionKnowledge}
	if !a.ready() {
		return s
	}
	files, chunks, ok := a.ws.Documents()
	switch {
	case !ok:
		s.Count = "off"
		s.Body = append(s.Body, boardLine{Name: "search is off", Note: "no embedding endpoint", Tone: toneDim})
		return s
	case files == 0:
		s.Count = "empty"
		s.Body = append(s.Body, boardLine{Name: "nothing indexed", Note: "put files in mnt/knowledge", Tone: toneDim})
		return s
	}
	s.Count = fmt.Sprintf("%d files", files)
	s.Body = append(s.Body, boardLine{Name: fmt.Sprintf("%d chunks", chunks), Note: "mnt/knowledge", Tone: toneDim})
	// The breakdown by top-level directory, not the first few paths. "specs/ 142" stays informative
	// at three files and at three thousand; a prefix of a list stops being informative at about ten.
	for _, line := range a.docDirs {
		if len(s.Body) == boardBodyCap {
			break
		}
		s.Body = append(s.Body, line)
	}
	return s
}

func (a *app) sectionMemory() boardSection {
	s := boardSection{ID: sectionMemory}
	if !a.ready() {
		return s
	}
	index := strings.TrimSpace(a.ws.Memory())
	if index == "" {
		s.Count = "empty"
		s.Body = append(s.Body, boardLine{Name: "no notes yet", Note: "the assistant writes these itself", Tone: toneDim})
		return s
	}
	notes := strings.Split(index, "\n")
	s.Count = fmt.Sprintf("%d notes", len(notes))

	// The gauge comes before the notes, because the ceiling is the more urgent fact. It is ENFORCED:
	// entries past it are dropped from every prompt, silently, and until now nothing on screen said
	// so. A catalog at 1.9K of 2K matters more than any two note titles.
	used, budget := len(index), a.ws.MemoryBudget()
	t := toneDim
	if used*100/max(budget, 1) >= 90 {
		t = toneWarn
	}
	s.Body = append(s.Body, boardLine{
		Bar:  gauge(used, budget, 14),
		Note: fmt.Sprintf("%s of %s in every prompt", bytesShort(used), bytesShort(budget)),
		Tone: t,
	})
	for _, n := range notes {
		if len(s.Body) == boardBodyCap {
			s.Body = append(s.Body, more(len(notes)-(boardBodyCap-1), "notes"))
			break
		}
		s.Body = append(s.Body, boardLine{Name: "", Note: strings.TrimSpace(n), Tone: toneDim})
	}
	return s
}

func (a *app) sectionSecrets() boardSection {
	s := boardSection{ID: sectionSecrets}
	if !a.ready() {
		return s
	}
	names := a.ws.Secrets()
	if a.ws.VaultLocked() {
		s.Count = "locked"
		s.Body = append(s.Body, boardLine{Name: "vault locked", Note: "no master key", Tone: toneBad})
		return s
	}
	s.Count = fmt.Sprintf("%d · unlocked", len(names))
	if len(names) == 0 {
		s.Body = append(s.Body, boardLine{Name: "none stored", Note: "nocturn secret set <name>", Tone: toneDim})
		return s
	}
	// Names only, never values. The rule the sandbox runs on, applied to the screen: that a
	// credential exists is something a person needs to know; what it says is not.
	shown := min(len(names), boardBodyCap)
	s.Body = append(s.Body, boardLine{Note: strings.Join(names[:shown], " · "), Tone: toneDim})
	if rest := len(names) - shown; rest > 0 {
		s.Body = append(s.Body, more(rest, "secrets"))
	}
	return s
}

// page is a section shown whole: nothing capped, nothing truncated.
// It is built ONCE per frame by the template and handed to whatever needs it — the knowledge page
// reads the corpus's paths off disk, and building it three times to lay out one screen is three
// directory walks for one picture.
func (a *app) page(id sectionID) subPage {
	if !a.inspectOpen.Get() {
		return subPage{}
	}
	if !a.ready() {
		return subPage{Summary: "opening…"}
	}
	switch id {
	case sectionAgents:
		return a.pageAgents()
	case sectionMCP:
		return a.pageMCP()
	case sectionExtensions:
		return a.pageExtensions()
	case sectionTools:
		return a.pageTools()
	case sectionKnowledge:
		return a.pageKnowledge()
	case sectionMemory:
		return a.pageMemory()
	case sectionSecrets:
		return a.pageSecrets()
	}
	return subPage{}
}

func (a *app) pageAgents() subPage {
	agents := a.ws.Agents()
	p := subPage{Summary: fmt.Sprintf("%d declared", len(agents))}
	width := a.inspectWidth()
	for _, ag := range agents {
		when := ag.When
		if when == "" {
			when = "no schedule · fired by hand"
		}
		b := pageBlock{
			Icon: "⏺", Tone: toneGood, Name: ag.Name, State: string(ag.Autonomy),
			Meta: when + " · effort " + string(ag.Effort) + " · budget " + ag.Budget.String(),
			Body: wrap(ag.Description, width),
		}
		if ag.Autonomy != "guarded" {
			b.Tone = toneDim
		}
		if len(ag.Tools) > 0 {
			b.Extra = strings.Join(ag.Tools, " · ")
		} else {
			b.Extra = "every tool in the workspace"
		}
		p.Blocks = append(p.Blocks, b)
	}
	return p
}

func (a *app) pageMCP() subPage {
	mcp := a.ws.Inventory().MCP
	up, tools := 0, 0
	for _, m := range mcp {
		if m.State == workspace.MCPConnected {
			up++
			tools += m.Tools
		}
	}
	p := subPage{Summary: fmt.Sprintf("%d declared · %d connected · %d tools contributed", len(mcp), up, tools)}
	width := a.inspectWidth()
	for _, m := range mcp {
		b := pageBlock{
			Name: m.Name, State: string(m.State),
			Meta: fmt.Sprintf("%s · %d tools", m.URL, m.Tools),
			// Whole. This is the string the view exists to deliver, and the page is the one place
			// with no cap on it at all: a long transport error becomes lines to scroll rather than
			// eighteen runes to guess at.
			Body: wrap(m.Note, width),
		}
		switch m.State {
		case workspace.MCPConnected:
			b.Icon, b.Tone = "⏺", toneGood
		case workspace.MCPFailed:
			b.Icon, b.Tone = "✗", toneBad
		default:
			b.Icon, b.Tone = "!", toneWarn
		}
		p.Blocks = append(p.Blocks, b)
	}
	return p
}

func (a *app) pageExtensions() subPage {
	inv := a.ws.Inventory()
	p := subPage{Summary: fmt.Sprintf("%d plugins · %d skills", len(inv.Plugins), len(inv.Skills))}
	for _, name := range inv.Plugins {
		p.Blocks = append(p.Blocks, pageBlock{
			Icon: "⏺", Tone: toneGood, Name: name, State: "plugin",
			Meta: "sandboxed · contributes tools",
		})
	}
	for _, name := range inv.Skills {
		p.Blocks = append(p.Blocks, pageBlock{
			Icon: "⏺", Tone: toneDim, Name: name, State: "skill",
			Meta: "instructions the model can load",
		})
	}
	return p
}

func (a *app) pageTools() subPage {
	tools := a.filtered(a.ws.Inventory().Tools)
	p := subPage{Columns: 3, Summary: a.countSummary(len(tools), len(a.ws.Inventory().Tools), "tools")}
	for _, t := range tools {
		p.Grid = append(p.Grid, gridCell{Left: t})
	}
	return p
}

func (a *app) pageKnowledge() subPage {
	all := a.ws.DocumentPaths()
	paths := a.filtered(all)
	files, chunks, _ := a.ws.Documents()
	p := subPage{
		Columns: 2,
		Summary: fmt.Sprintf("%d files · %d chunks · mnt/knowledge — %s",
			files, chunks, a.countSummary(len(paths), len(all), "match")),
	}
	for _, path := range paths {
		p.Grid = append(p.Grid, gridCell{Left: path})
	}
	return p
}

func (a *app) pageMemory() subPage {
	index := strings.TrimSpace(a.ws.Memory())
	var notes []string
	if index != "" {
		notes = strings.Split(index, "\n")
	}
	shown := a.filtered(notes)
	used, budget := len(index), a.ws.MemoryBudget()

	p := subPage{Summary: fmt.Sprintf("%s of %s in every prompt · %s",
		bytesShort(used), bytesShort(budget), a.countSummary(len(shown), len(notes), "notes"))}
	// The drop is stated rather than left to be inferred. Past the ceiling the catalog is silently
	// truncated at build time, and a reader who cannot see that is reading a list that lies.
	if used >= budget {
		p.Blocks = append(p.Blocks, pageBlock{
			Icon: "✗", Tone: toneBad, Name: "at the ceiling", State: "notes are being dropped",
			Body: wrap("the catalog is built until the budget runs out and then stops — notes past it "+
				"never reach the model. Shorten some descriptions, or remove notes that have stopped "+
				"being true.", a.inspectWidth()),
		})
	}
	for _, n := range shown {
		p.Blocks = append(p.Blocks, pageBlock{Tone: toneDim, Body: wrap(strings.TrimSpace(n), a.inspectWidth())})
	}
	return p
}

func (a *app) pageSecrets() subPage {
	if a.ws.VaultLocked() {
		return subPage{Summary: "the vault is locked — no master key, so not even the names are known"}
	}
	names := a.ws.Secrets()
	p := subPage{Columns: 3, Summary: fmt.Sprintf("%d stored · names only, never values", len(names))}
	for _, n := range names {
		p.Grid = append(p.Grid, gridCell{Left: n})
	}
	return p
}

// filtered narrows a list by the page's filter: case-insensitive substring, no ranking. The lists it
// serves are alphabetical, and a clever order would only make them unpredictable.
func (a *app) filtered(all []string) []string {
	q := strings.ToLower(strings.TrimSpace(a.inspectFilter.Get()))
	if q == "" {
		return all
	}
	out := make([]string, 0, len(all))
	for _, s := range all {
		if strings.Contains(strings.ToLower(s), q) {
			out = append(out, s)
		}
	}
	return out
}

// countSummary says how much of a list is on screen, and only mentions the filter when one is on.
func (a *app) countSummary(shown, total int, noun string) string {
	if shown == total {
		return fmt.Sprintf("%d %s", total, noun)
	}
	return fmt.Sprintf("%d of %d %s", shown, total, noun)
}

// familyLines groups tool names by the part before their first underscore and reports the biggest
// families first. The prefix convention is real in this codebase — file_, http_, remind_, memory_ —
// and grouping by it is how "can it reach the network" is answered without reading every name.
func familyLines(tools []string, lines int) []string {
	if len(tools) == 0 || lines <= 0 {
		return nil
	}
	counts := map[string]int{}
	for _, t := range tools {
		name, _, ok := strings.Cut(t, "_")
		if !ok {
			name = t
		}
		counts[name]++
	}
	families := make([]string, 0, len(counts))
	for name := range counts {
		families = append(families, name)
	}
	// By size, then by name: a stable order, so the board does not reshuffle when two families draw.
	sort.Slice(families, func(i, j int) bool {
		if counts[families[i]] != counts[families[j]] {
			return counts[families[i]] > counts[families[j]]
		}
		return families[i] < families[j]
	})

	const perLine = 5
	var out []string
	for i := 0; i < len(families) && len(out) < lines; i += perLine {
		end := min(i+perLine, len(families))
		parts := make([]string, 0, perLine)
		for _, f := range families[i:end] {
			parts = append(parts, fmt.Sprintf("%s %d", f, counts[f]))
		}
		line := strings.Join(parts, " · ")
		// The last line this section has room for carries whatever it could not show.
		if len(out) == lines-1 && end < len(families) {
			line += fmt.Sprintf(" · +%d more", len(families)-end)
		}
		out = append(out, line)
	}
	return out
}

// docDirLines is the knowledge base by top-level directory, biggest first. Computed from the paths
// once and kept, because the paths are a disk read and the board no longer shows them.
func docDirLines(paths []string, lines int) []boardLine {
	if len(paths) == 0 || lines <= 0 {
		return nil
	}
	counts := map[string]int{}
	for _, p := range paths {
		dir := path.Dir(p)
		if dir == "." || dir == "/" {
			dir = "(root)"
		} else if i := strings.IndexByte(dir, '/'); i > 0 {
			dir = dir[:i] // the TOP level only; deeper is the page's business
		}
		counts[dir+"/"]++
	}
	dirs := make([]string, 0, len(counts))
	for d := range counts {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool {
		if counts[dirs[i]] != counts[dirs[j]] {
			return counts[dirs[i]] > counts[dirs[j]]
		}
		return dirs[i] < dirs[j]
	})

	const perLine = 4
	var out []boardLine
	for i := 0; i < len(dirs) && len(out) < lines; i += perLine {
		end := min(i+perLine, len(dirs))
		parts := make([]string, 0, perLine)
		for _, d := range dirs[i:end] {
			parts = append(parts, fmt.Sprintf("%s %d", d, counts[d]))
		}
		line := strings.Join(parts, " · ")
		if len(out) == lines-1 && end < len(dirs) {
			line += fmt.Sprintf(" · +%d more", len(dirs)-end)
		}
		out = append(out, boardLine{Note: line, Tone: toneDim})
	}
	return out
}

// more is the line that says what was left out. Every capped list ends in one, so a cap is never
// silent — a list that stops without saying so reads as a list that is complete.
func more(n int, noun string) boardLine {
	return boardLine{Note: fmt.Sprintf("+%d more %s", n, noun), Tone: toneDim}
}

// gauge draws a ratio. Filled from the left, so it reads the way every other bar does.
func gauge(used, budget, width int) string {
	if budget <= 0 || width <= 0 {
		return ""
	}
	full := min(used*width/budget, width)
	return strings.Repeat("█", full) + strings.Repeat("░", width-full)
}

// bytesShort renders a byte count the way a person says it.
func bytesShort(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fK", float64(n)/1024)
}

// inspectWidth is the usable width inside the view: the window, less the modal's padding, the pane's
// border and padding, and the column the scrollbar draws in.
func (a *app) inspectWidth() int {
	return max(a.width()-2-2-2-scrollbarGap, 40)
}

// boardColumns is how many sections sit side by side. Two above a hundred columns, one below —
// where a second column would leave each too narrow to hold a name and its metric.
func (a *app) boardColumns() int {
	if a.inspectWidth() < 100 {
		return 1
	}
	return 2
}

// boardColWidth is one section's share, less the gap between them.
func (a *app) boardColWidth() int {
	cols := a.boardColumns()
	return (a.inspectWidth() - (cols-1)*2) / cols
}

// boardNameWidth is the first column inside a section body, so names line up down the box and the
// notes beside them start in the same place.
func (a *app) boardNameWidth() int { return min(18, a.boardColWidth()/2) }

// refreshInspect rebuilds what the view draws. Called when it opens and on the second-tick while it
// is open, so an MCP server that reconnects or a note just written shows up without closing it.
//
// The document paths are the exception: reading them is a disk walk, and the board shows a
// breakdown rather than the list. So they are re-read only when the FILE COUNT has changed, which is
// two cheap integers from the index. What this replaces walked the corpus once a second to build a
// list nobody was looking at.
func (a *app) refreshInspect() {
	if !a.inspectOpen.Get() || !a.ready() {
		return
	}
	files, _, ok := a.ws.Documents()
	if ok && files != a.docFiles {
		a.docFiles = files
		a.docDirs = docDirLines(a.ws.DocumentPaths(), boardBodyCap-1)
	}
}

// toggleInspect opens or closes the view. Opening moves no focus of its own: the modal traps it, and
// trapping means the framework pulls the keyboard onto the first station inside the overlay — which
// is the pane, the only focusable thing in there. Closing asks for the composer explicitly rather
// than trusting the modal's restore-by-index, because what is under the overlay may have changed
// while it was up.
func (a *app) toggleInspect() {
	open := !a.inspectOpen.Get()
	a.inspectOpen.Set(open)
	if open {
		a.openSection(sectionBoard)
		a.docFiles = -1 // force the first read
		a.refreshInspect()
		return
	}
	if !a.readOnly() {
		a.focusOn(a.composer)
	}
}

// openSection switches pages and starts the new one at the top with no filter. The filter belongs to
// the page it was typed on: carried across it would hide most of a list the reader never narrowed.
func (a *app) openSection(id sectionID) {
	a.inspectSection.Set(id)
	a.inspectFilter.Set("")
	a.inspectTyping.Set(false)
	a.inspectScroll.Set(0)
}

// workspaceName is the identity line's left half.
func (a *app) workspaceName() string {
	if !a.ready() {
		return "opening…"
	}
	return a.ws.Name()
}

// capabilitySummary is its right half: the model, and the one number that says how much this
// assistant can reach at all.
func (a *app) capabilitySummary() string {
	if !a.ready() {
		return a.model
	}
	return fmt.Sprintf("%s · %d tools", a.model, len(a.ws.Inventory().Tools))
}

// problemSummary titles the verdict box by counting the two kinds apart: a failure is something
// broken, an errand is something a person can go and do. It takes the list rather than rebuilding
// it, for the same reason page does.
func problemSummary(probs []problem) string {
	bad, errands := 0, 0
	for _, p := range probs {
		if p.Bad {
			bad++
		} else {
			errands++
		}
	}
	parts := make([]string, 0, 2)
	if bad > 0 {
		parts = append(parts, plural(bad, "problem"))
	}
	if errands > 0 {
		parts = append(parts, plural(errands, "errand"))
	}
	return " " + strings.Join(parts, " · ") + " "
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// boardRows deals the sections into rows of boardColumns. Down the rows in a FIXED order, not packed
// by height: an even bottom edge is worth less than knowing where a thing lives without looking.
func (a *app) boardRows() [][]boardSection {
	cols := a.boardColumns()
	all := a.board()
	rows := make([][]boardSection, 0, (len(all)+cols-1)/cols)
	for i := 0; i < len(all); i += cols {
		rows = append(rows, all[i:min(i+cols, len(all))])
	}
	return rows
}

// pageCellWidth is one cell of a grid page.
func (a *app) pageCellWidth(p subPage) int {
	return a.inspectWidth() / max(p.Columns, 1)
}

// pageGridRows deals a grid page's cells into rows.
func pageGridRows(p subPage) [][]gridCell {
	cols := max(p.Columns, 1)
	rows := make([][]gridCell, 0, (len(p.Grid)+cols-1)/cols)
	for i := 0; i < len(p.Grid); i += cols {
		rows = append(rows, p.Grid[i:min(i+cols, len(p.Grid))])
	}
	return rows
}

// inspectKeys drive the view. All preempt, for the reason the other overlays' keys do: a trapping
// modal ends its own KeyMap with a catch-all matching everything, and the preempt pass is the only
// one that runs before it.
//
// Digits rather than a cursor. A cursor over a two-column board would need its own h/j/k/l and would
// fight the pane for j and k; a digit is one keystroke from anywhere, and it is already this UI's
// idiom — the approval offers the same.
func (a *app) inspectKeys() tui.KeyMap {
	// While the filter is being typed it owns the keyboard, digits included: a page filtered to
	// "2fa" must not jump to section 2 halfway through the word.
	if a.inspectTyping.Get() {
		return tui.KeyMap{
			tui.OnPreemptStop(tui.KeyEscape, func(tui.KeyEvent) { a.clearFilter() }),
			tui.OnPreemptStop(tui.KeyEnter, func(tui.KeyEvent) { a.inspectTyping.Set(false) }),
			tui.OnPreemptStop(tui.KeyBackspace, func(tui.KeyEvent) { a.typeFilter(0, true) }),
			tui.OnPreemptStop(tui.AnyRune, func(ke tui.KeyEvent) { a.typeFilter(ke.Rune, false) }),
		}
	}
	km := tui.KeyMap{
		tui.OnPreemptStop(tui.KeyEscape, func(tui.KeyEvent) { a.escapeInspect() }),
		tui.OnPreemptStop(tui.Rune('k').Ctrl(), func(tui.KeyEvent) { a.toggleInspect() }),
	}
	if a.inspectSection.Get().Filterable() {
		km = append(km, tui.OnPreemptStop(tui.Rune('/'), func(tui.KeyEvent) { a.inspectTyping.Set(true) }))
	}
	for id := sectionAgents; id < sectionCount; id++ {
		km = append(km, tui.OnPreemptStop(tui.Rune(rune('0'+id)), func(tui.KeyEvent) { a.openSection(id) }))
	}
	return km
}

// escapeInspect peels one layer. Escape from a page goes back to the board rather than closing
// outright: opening the wrong section is the ordinary mistake, and it must cost one key. Ctrl+K is
// the way out from any depth.
func (a *app) escapeInspect() {
	if a.inspectSection.Get() == sectionBoard {
		a.toggleInspect()
		return
	}
	a.openSection(sectionBoard)
}

func (a *app) typeFilter(r rune, back bool) {
	q := a.inspectFilter.Get()
	if back {
		if runes := []rune(q); len(runes) > 0 {
			q = string(runes[:len(runes)-1])
		}
	} else {
		q += string(r)
	}
	a.inspectFilter.Set(q)
	a.inspectScroll.Set(0)
}

// clearFilter empties the filter and stops typing, but stays on the page — Escape here undoes the
// narrowing, not the navigation.
func (a *app) clearFilter() {
	a.inspectFilter.Set("")
	a.inspectTyping.Set(false)
	a.inspectScroll.Set(0)
}

// inspectTitle is the breadcrumb in the pane's border: where you are, and how you got there.
func (a *app) inspectTitle() string {
	if id := a.inspectSection.Get(); id != sectionBoard {
		return " workspace › " + id.Title() + " "
	}
	return " workspace "
}
