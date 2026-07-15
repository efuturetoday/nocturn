package tool

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// Phase marks whether an Event is the start or the end of an invocation.
type Phase int

const (
	Start Phase = iota
	End
)

// Event is emitted by a Registry around every tool invocation — model- or
// script-issued. ID is unique per invocation and pairs a Start with its
// End; Parent is the enclosing invocation's ID (0 = root), so an observer can
// reconstruct both concurrency (independent roots run at once) and nesting (a
// script's nocturn.call carries its code.run's ID as Parent). Because calls may
// run concurrently, events from different invocations interleave — match them by
// ID, never by arrival order.
type Event struct {
	ID     uint64 // unique per invocation
	Parent uint64 // enclosing invocation's ID; 0 = root
	Tool   string
	Args   string // JSON, as the caller supplied it (model args or script args)
	Phase  Phase
	Result string // End only
	Err    error  // End only (e.g. gateway.ErrDenied for a denied effect)
}

// Registry is the one place tool calls are dispatched: it maps names to Tools,
// hands their specs to the Model, and runs a named tool's Invoke. It is shared
// by the Brain (model-issued calls) and the script interpreter (script-issued
// calls), so its OnCall observer sees every tool call from both. The tools map is
// mutated at runtime (plugins install/uninstall tools), so it is guarded by mu;
// call ids come from an atomic counter. OnCall is set once at wiring.
type Registry struct {
	mu     sync.RWMutex
	tools  map[string]Tool
	nextID atomic.Uint64
	OnCall func(Event) // observability sink; nil = off
}

// callIDKey carries the enclosing invocation's id so a nested Invoke (a script's
// nocturn.call inside code.run) can record its parent.
type callIDKey struct{}

func withCallID(ctx context.Context, id uint64) context.Context {
	return context.WithValue(ctx, callIDKey{}, id)
}

func callIDFrom(ctx context.Context) uint64 {
	id, _ := ctx.Value(callIDKey{}).(uint64)
	return id
}

// NewRegistry builds a Registry over the given tools. A nil slice yields an empty
// registry (every call reports "unknown tool"), which is convenient for tests.
func NewRegistry(tools []Tool) *Registry {
	reg := make(map[string]Tool, len(tools))
	for _, t := range tools {
		reg[t.Name] = t
	}
	return &Registry{tools: reg}
}

// Select returns a new Registry holding only the tools whose name satisfies
// keep, sharing this registry's observability sink. It snapshots the current
// tools — used to give an agent a registry limited to the tools it may use, so a
// tool outside its list is not merely hidden from the model but UNREACHABLE
// (Invoke reports "unknown tool"), a hard bound rather than a presentation filter.
func (r *Registry) Select(keep func(name string) bool) *Registry {
	r.mu.RLock()
	sub := make(map[string]Tool)
	for name, t := range r.tools {
		if keep(name) {
			sub[name] = t
		}
	}
	r.mu.RUnlock()
	return &Registry{tools: sub, OnCall: r.OnCall}
}

// Add registers a tool after construction — code.run, or a plugin's tools.
func (r *Registry) Add(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name] = t
}

// Remove unregisters a tool (plugin uninstall). A missing name is a no-op.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// Has reports whether a tool name is registered (collision check on install).
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// MaxResult returns the registered tool's per-result byte budget override, or 0
// if the tool is unknown or sets none (the caller then applies its own default).
func (r *Registry) MaxResult(name string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name].MaxResult
}

// Specs returns the tool declarations for the Model, sorted by name.
func (r *Registry) Specs() []Spec {
	r.mu.RLock()
	specs := make([]Spec, 0, len(r.tools))
	for _, t := range r.tools {
		specs = append(specs, t.Spec)
	}
	r.mu.RUnlock()
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

// Invoke looks up a tool by name and runs it, emitting a Start before and a
// End after (carrying the result/error). An unknown tool is reported as an
// error the caller can surface — not fatal. The observer is fail-open.
func (r *Registry) Invoke(ctx context.Context, name, args string) (out string, err error) {
	id := r.nextID.Add(1)
	parent := callIDFrom(ctx)
	ctx = withCallID(ctx, id) // so a nested Invoke records this call as its parent
	r.emit(Event{ID: id, Parent: parent, Tool: name, Args: args, Phase: Start})
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock() // release before Invoke: it may run long and re-enter the registry
	if !ok {
		err = errors.New("unknown tool " + name)
	} else {
		out, err = t.Invoke(ctx, args)
	}
	r.emit(Event{ID: id, Parent: parent, Tool: name, Args: args, Phase: End, Result: out, Err: err})
	return out, err
}

func (r *Registry) emit(ev Event) {
	if r.OnCall != nil {
		r.OnCall(ev)
	}
}
