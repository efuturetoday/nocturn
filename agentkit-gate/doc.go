// Package gate adds human-approved, remembered permission to agentkit tools. It is a thin layer on
// the policy-blind core: a tool action is checked against a Policy (allow / ask / deny); an "ask"
// first consults remembered Grants and, if none covers it, prompts a human out-of-band via an
// Approver; the answer is remembered — for this session, or always.
//
// Two things are separate on purpose:
//   - WHICH tools an agent has at all is agentkit's ToolSet (Select) — a static bound set once.
//   - WHAT a tool may DO (this package) — checked per action, asked when risky, remembered.
//
// The machinery is installed into ctx with With and flows to nested tool calls and sub-agents
// automatically, so one install covers the whole tree. There is no separate host concept: a host
// allowlist is just Grants whose Target is the host (use a shared Tool name like "net" so one grant
// covers every network tool).
package gate
