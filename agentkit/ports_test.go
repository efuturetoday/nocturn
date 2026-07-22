package agentkit_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
)

// --- schema.go ---

func TestParseSchema_RoundTripSupportedSubset(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"description": "a thing",
		"minProperties": 2,
		"properties": {
			"name": {"type": "string", "enum": ["a", "b"]},
			"tags": {"type": "array", "items": {"type": "string"}}
		},
		"required": ["name"]
	}`)
	s, err := agentkit.ParseSchema(raw)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	if s.Type != agentkit.TypeObject || s.Description != "a thing" {
		t.Fatalf("type/description = %q/%q", s.Type, s.Description)
	}
	if len(s.Required) != 1 || s.Required[0] != "name" {
		t.Fatalf("required = %v, want [name]", s.Required)
	}
	name := s.Properties["name"]
	if name == nil || name.Type != agentkit.TypeString || len(name.Enum) != 2 {
		t.Fatalf("name property = %+v", name)
	}
	tags := s.Properties["tags"]
	if tags == nil || tags.Type != agentkit.TypeArray || tags.Items == nil || tags.Items.Type != agentkit.TypeString {
		t.Fatalf("tags property = %+v", tags)
	}
	// Unsupported keyword (minProperties) is simply not represented — allowlist by construction.
}

func TestParseSchema_EmptyYieldsNil(t *testing.T) {
	s, err := agentkit.ParseSchema(nil)
	if err != nil || s != nil {
		t.Fatalf("ParseSchema(nil) = (%v, %v), want (nil, nil)", s, err)
	}
}

func TestParseSchema_MalformedError(t *testing.T) {
	if _, err := agentkit.ParseSchema(json.RawMessage(`{bad`)); err == nil {
		t.Fatal("ParseSchema of malformed JSON: err = nil, want error")
	}
}

func TestObject_Prop_Require_Chaining(t *testing.T) {
	s := agentkit.Object(
		agentkit.Prop("a", agentkit.String("d1")),
		agentkit.Prop("b", agentkit.Number("d2")),
	).Require("a")
	if s.Type != agentkit.TypeObject {
		t.Fatalf("type = %q", s.Type)
	}
	if len(s.Properties) != 2 || s.Properties["a"] == nil || s.Properties["b"] == nil {
		t.Fatalf("properties = %+v", s.Properties)
	}
	if len(s.Required) != 1 || s.Required[0] != "a" {
		t.Fatalf("required = %v, want [a]", s.Required)
	}
}

func TestString_WithEnum(t *testing.T) {
	s := agentkit.String("choose").WithEnum("x", "y", "z")
	if s.Type != agentkit.TypeString || len(s.Enum) != 3 || s.Enum[0] != "x" {
		t.Fatalf("enum schema = %+v", s)
	}
}

// --- skill.go ---

func TestSkill_Validate(t *testing.T) {
	long := strings.Repeat("d", agentkit.MaxSkillDescLen+1)
	tests := []struct {
		name    string
		skill   agentkit.Skill
		wantErr bool
	}{
		{"valid", agentkit.Skill{Name: "pdf-tools", Description: "d"}, false},
		{"bad name uppercase", agentkit.Skill{Name: "PDF", Description: "d"}, true},
		{"bad name underscore", agentkit.Skill{Name: "pdf_tools", Description: "d"}, true},
		{"reserved anthropic", agentkit.Skill{Name: "anthropic-x", Description: "d"}, true},
		{"reserved claude", agentkit.Skill{Name: "claude-x", Description: "d"}, true},
		{"empty description", agentkit.Skill{Name: "pdf", Description: ""}, true},
		{"overlong description", agentkit.Skill{Name: "pdf", Description: long}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.skill.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate(%+v) err = %v, wantErr %v", tt.skill, err, tt.wantErr)
			}
		})
	}
}

func TestNewSkillSet_DuplicateName_Error(t *testing.T) {
	_, err := agentkit.NewSkillSet(
		agentkit.Skill{Name: "dup", Description: "a"},
		agentkit.Skill{Name: "dup", Description: "b"},
	)
	if err == nil {
		t.Fatal("duplicate skill name: err = nil, want error")
	}
}

func TestSkillSet_LoadTool_ReturnsBody(t *testing.T) {
	set, err := agentkit.NewSkillSet(agentkit.Skill{Name: "pdf", Description: "d", Body: "the body"})
	if err != nil {
		t.Fatalf("NewSkillSet: %v", err)
	}
	load := set.LoadTool()

	t.Run("known skill returns body", func(t *testing.T) {
		out, err := load.Call(context.Background(), `{"name":"pdf"}`)
		if err != nil {
			t.Fatalf("Call err = %v", err)
		}
		if out != "the body" {
			t.Fatalf("body = %q, want %q", out, "the body")
		}
	})
	t.Run("unknown skill errors", func(t *testing.T) {
		if _, err := load.Call(context.Background(), `{"name":"nope"}`); err == nil {
			t.Fatal("unknown skill: err = nil, want error")
		}
	})
	t.Run("malformed args error", func(t *testing.T) {
		if _, err := load.Call(context.Background(), `{bad`); err == nil {
			t.Fatal("malformed args: err = nil, want error")
		}
	})
}

func TestSkillSet_Specs_SortedBodiesOmitted(t *testing.T) {
	set, err := agentkit.NewSkillSet(
		agentkit.Skill{Name: "zebra", Description: "z", Body: "zbody"},
		agentkit.Skill{Name: "alpha", Description: "a", Body: "abody"},
	)
	if err != nil {
		t.Fatalf("NewSkillSet: %v", err)
	}
	specs := set.Specs()
	if len(specs) != 2 || specs[0].Name != "alpha" || specs[1].Name != "zebra" {
		t.Fatalf("specs not sorted: %+v", specs)
	}
	for _, s := range specs {
		if s.Body != "" {
			t.Fatalf("spec %q leaks a body: %q", s.Name, s.Body)
		}
	}
}

// --- store.go ---

func TestMemStore_SaveLoad_CopySemantics(t *testing.T) {
	store := agentkit.NewMemStore()
	orig := []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}}
	if err := store.Save("id", orig); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Mutating the caller's slice must not touch stored state.
	orig[0].Content = "mutated"

	got, err := store.Load("id")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].Content != "hi" {
		t.Fatalf("Load = %+v, want the copy {user hi}", got)
	}
	// Mutating the loaded copy must not touch stored state either.
	got[0].Content = "changed"
	again, _ := store.Load("id")
	if again[0].Content != "hi" {
		t.Fatalf("second Load = %q, want hi (Load returns a fresh copy)", again[0].Content)
	}
}

func TestMemStore_Load_UnknownID_NilNoError(t *testing.T) {
	got, err := agentkit.NewMemStore().Load("nope")
	if err != nil || got != nil {
		t.Fatalf("Load(unknown) = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestMemStore_List_Sorted(t *testing.T) {
	store := agentkit.NewMemStore()
	for _, id := range []string{"c", "a", "b"} {
		if err := store.Save(id, nil); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	ids, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("List = %v, want %v", ids, want)
	}
}

// --- tokenizer.go ---

func TestApproxTokenizer_Count(t *testing.T) {
	tk := agentkit.ApproxTokenizer()
	tests := []struct {
		name string
		text string
		want int // (runeCount + 3) / 4
	}{
		{"empty is zero", "", 0},
		{"four ascii runes", "abcd", 1},        // (4+3)/4 = 1
		{"seven ascii runes", "abcdefg", 2},    // (7+3)/4 = 2
		{"multibyte counts runes", "日本語ab", 2}, // 5 runes → (5+3)/4 = 2, not by bytes
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tk.Count(tt.text)
			if err != nil {
				t.Fatalf("Count(%q) err = %v, want nil (never errors)", tt.text, err)
			}
			if got != tt.want {
				t.Fatalf("Count(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}

// --- diagnostic.go ---

func TestDiagnostics_ConcurrentFeeders(t *testing.T) {
	var d agentkit.Diagnostics
	const feeders, each = 8, 50
	var wg sync.WaitGroup
	for i := range feeders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				d.Warn("subject", "msg")
				_ = i
			}
		}()
	}
	wg.Wait()
	if got := d.Len(); got != feeders*each {
		t.Fatalf("Len = %d, want %d", got, feeders*each)
	}
	if len(d.All()) != feeders*each {
		t.Fatalf("All len = %d, want %d", len(d.All()), feeders*each)
	}
}

func TestDiagnostics_HasErrors(t *testing.T) {
	var d agentkit.Diagnostics
	d.Info("s", "i")
	d.Warn("s", "w")
	if d.HasErrors() {
		t.Fatal("HasErrors = true with only info/warn, want false")
	}
	d.Error("s", "e")
	if !d.HasErrors() {
		t.Fatal("HasErrors = false after an Error, want true")
	}
}

func TestDiagnose_NoCollector_NoOp(t *testing.T) {
	// Fail-open: no collector attached → no panic.
	agentkit.Diagnose(context.Background(), agentkit.Warn, "s", "m")
	if d := agentkit.DiagnosticsFrom(context.Background()); d != nil {
		t.Fatalf("DiagnosticsFrom(bg) = %v, want nil", d)
	}
}

func TestLevel_String(t *testing.T) {
	tests := []struct {
		level agentkit.Level
		want  string
	}{
		{agentkit.Info, "info"},
		{agentkit.Warn, "warn"},
		{agentkit.Error, "error"},
		{agentkit.Level(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Fatalf("Level(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

// --- logger.go ---

func TestSlogLogger_NilYieldsNop(t *testing.T) {
	if reflect.TypeOf(agentkit.SlogLogger(nil)) != reflect.TypeOf(agentkit.NopLogger()) {
		t.Fatal("SlogLogger(nil) is not the NopLogger type")
	}
}

func TestNopLogger_Discards(t *testing.T) {
	l := agentkit.NopLogger()
	l.Debug("d")
	l.Info("i")
	l.Warn("w")
	l.Error("e")
	if l.With("k", "v") == nil {
		t.Fatal("With returned nil")
	}
	if l.WithContext(context.Background()) == nil {
		t.Fatal("WithContext returned nil")
	}
}

// --- compile-time port assertions ---
// (approxTokenizer/nopLogger are unexported so their concrete asserts live in production code;
// gate.Grants/MemGrants sit in a separate module. MemStore is the externally-assertable one.)

var _ agentkit.Store = (*agentkit.MemStore)(nil)

// --- core is HITL-agnostic ---

func TestCore_NoHITL_WithoutGate(t *testing.T) {
	// A deny-worthy action runs FREE when no gate machinery is installed: the agentkit core has no
	// notion of approval, so a tool simply executes. (Gating is a separate, opt-in layer.)
	var ran bool
	tool := newTool(t, "act", func(context.Context, string) (string, error) {
		ran = true
		return "did it", nil
	})
	set := newSet(t, tool)
	llm := &stepLLM{steps: []agentkit.Step{callStep("a", "act", "{}"), answerStep("done")}}
	if _, err := agentkit.Once(context.Background(), llm, "go", agentkit.WithTools(set)); err != nil {
		t.Fatalf("Once err = %v", err)
	}
	if !ran {
		t.Fatal("tool did not run; core should be HITL-agnostic without a gate")
	}
}
