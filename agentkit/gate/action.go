package gate

// Action is the thing being gated: a Kind — a tool name, or a shared axis such as "net" for a host
// allowlist — and, when the action reaches one, the runtime Target (a host, a path). Target is "" for
// a Kind with no target. Gating on a shared Kind (instead of each tool's own name) lets one grant
// cover every tool on that axis.
type Action struct {
	Kind   string
	Target string
}
