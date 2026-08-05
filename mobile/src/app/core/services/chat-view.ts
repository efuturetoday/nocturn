/**
 * The app-internal VIEW shapes a chat renders — assembled by ChatService from the wire protocol
 * (`Message`/`ToolNode`/`ChatTool`). The live stream AND a snapshot both reduce into these, so they
 * live on their own rather than hanging off a service: the templates, the reducer and the pure
 * `chat-snapshot` builders all share them.
 */

import type { ApprovalOption } from '../protocol/nocturn-protocol';

/** A rendered tool call — live (streamed, has phase/err) or from a snapshot (finished). */
export interface ToolView {
  key: string; // stable track id
  tool: string;
  args?: string;
  result?: string;
  err?: string;
  running: boolean;
  depth: number; // nesting: 0 = top-level call, >0 = sub-agent call (indent)
  id?: number; // the call id (live) — matches approval.request.frame
  parentId?: number; // the enclosing call's id (live) — lets the parked branch be walked to its ancestors
  startedAt?: number; // ms epoch when the call started (live only) — drives the ticking timer WHILE running
  durationMs?: number; // final wall-clock (server-measured): set on the end frame + restored from a snapshot
}

/** A rendered conversation message: user text, or an assistant turn with reasoning + tools. */
export interface ChatMessageView {
  role: 'user' | 'assistant';
  content: string;
  thinking: string; // dim reasoning, assistant only
  tools: ToolView[];
  error?: string;
  pending: boolean; // assistant turn still streaming
}

/** An open out-of-band approval the user must answer before the parked tool proceeds. `kind` and
 * `target` are the gate action verbatim — the sheet does the wording. */
export interface PendingApproval {
  frame?: number; // the tool call this approval is for — its tool-frame freezes its timer
  id: string;
  chatId?: string; // the chat/agent run whose turn raised this — for provenance (absent = not chat-scoped)
  kind: string;
  target?: string;
  options: ApprovalOption[];
}
