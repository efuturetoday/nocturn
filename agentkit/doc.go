// Package agentkit is a zero-dependency toolkit for building AI agents and chat sessions: an
// LLM-agnostic turn loop that drives a model through tool calls, with immutable tool and skill
// sets, sub-agents (a sub-agent is just a tool), a one-way event stream, and per-turn and
// per-tree guards (steps, wall-clock, tokens, spawn depth/population).
//
// Everything external is a port: the model (LLM), tools (Tool), the logger (Logger) and transcript
// storage (Store). The core imports nothing outside the standard library; provider adapters (e.g.
// OpenAI) live in separate modules. See DOCS.md for the full design.
package agentkit
