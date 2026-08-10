package workspace

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/memory"
)

// This file builds what the model is told about itself: the workspace persona (or an agent's
// instructions) plus the live memory index, folded together per turn.

// defaultPersona is the built-in system prompt used when a workspace has no PERSONA.md.
const defaultPersona = "You are nocturn, a concise, helpful assistant. Reach for a tool when it answers " +
	"the question better than you can, and say what you did in one line rather than narrating a plan first."

// composePrompt builds a session's system prompt: the base identity (the workspace persona, or an
// agent's instructions) plus the live memory index. has reports whether a name is in the runner's
// cage, so the block is omitted for a runner that cannot touch memory at all — showing an agent
// facts it has no tool to maintain would burn tokens and leak the user's notes into a cage that was
// deliberately narrowed.
//
// An empty memory yields the base prompt unchanged: a fresh workspace pays nothing, and the model
// still learns the capability exists from memory_write's description.
func composePrompt(base string, mem *memory.Store, has func(name string) bool) string {
	canUseMemory := has("memory_read") || has("memory_write")
	if mem == nil || !canUseMemory {
		return base
	}
	index := mem.Index()
	if index == "" {
		return base
	}
	const openTag = "\n\n<memory note=\"what you have chosen to remember about this user; " +
		"the links are files you can memory_read\">\n"
	return base + openTag + index + "\n</memory>"
}

// hasTool adapts a ToolSet to composePrompt's membership test.
func hasTool(ts agentkit.ToolSet) func(string) bool {
	return func(name string) bool {
		_, ok := ts[name]
		return ok
	}
}

// resolvePersona returns the workspace system prompt: the PERSONA.md override in the workspace root
// if present and non-empty, else defaultPersona. PERSONA.md lives in the ROOT — control-plane, never
// a tool-reachable path — so the model can neither read nor rewrite its own identity; a self-writable
// persona would be a prompt-injection vector onto the assistant itself.
func resolvePersona(dir string, log *slog.Logger) string {
	data, err := os.ReadFile(filepath.Join(dir, "PERSONA.md"))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			// A real read error (permissions, I/O) on an existing PERSONA.md must not
			// silently swap in the wrong identity — surface it rather than mask it.
			log.Warn("workspace: reading PERSONA.md, using default", "dir", dir, "err", err)
		}
		return defaultPersona
	}
	if body := strings.TrimSpace(string(data)); body != "" {
		return body
	}
	return defaultPersona
}
