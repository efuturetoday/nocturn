// Package agentkit is a zero-dependency toolkit for building AI agents and chat sessions: an
// LLM-agnostic turn loop that drives a model through tool calls, with immutable tool and skill
// sets, sub-agents (a sub-agent is just a tool), a one-way event stream, and per-turn and
// per-tree guards (steps, wall-clock, tokens, spawn depth/population).
//
// Immutable is about each set, not about the binding. A session asks for its tools, skills and system
// prompt once per turn (WithToolsFunc and friends), so a consumer whose available tools change while
// a conversation is open is answered by the next turn — while the turn already running keeps the set
// it was handed, because the model plans against the list it was given.
//
// Everything external is a port: the model (LLM), tools (Tool), the logger (Logger) and transcript
// storage (Store). The core imports nothing outside the standard library; provider adapters (e.g.
// OpenAI) live in separate modules. See DOCS.md for the full design.
package agentkit
