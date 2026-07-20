package gate

// Action is the thing being gated: the tool being called and, if it reaches one, the runtime target
// (a host, a path). Target is "" for a tool with no external target. Use a shared Tool name (e.g.
// "net") for a cross-tool axis such as a host allowlist, so one grant covers every tool on that axis.
type Action struct {
	Tool   string
	Target string
}
