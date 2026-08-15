import type { ChatSnapshot, Message, ServerEvent, ChatTool, ToolNode } from '../protocol/nocturn-protocol';
import type { ChatMessageView, ToolView } from './chat-view';

/**
 * The ONE pure fold of a chat's rendered state — no Angular, no signals. Both entry points reduce
 * into the SAME `ChatView` by the SAME builders, so the snapshot path and the live-stream path can
 * never drift:
 *   • `seed(snapshot)` — the initial state from a `chat.snapshot` (persisted transcript + per-turn
 *     tool forest + the running turn).
 *   • `applyEvent(view, event, now)` — one live event folded onto the state.
 * `ChatService` is a thin signal shell over these; the templates bind its derived view. Because this
 * module is data-in/data-out it is unit-testable in isolation, and the convergence test (a live
 * sequence vs the snapshot of its end state) pins the two paths together.
 */

/** The assembled render state of the one active chat. */
export interface ChatView {
  messages: ChatMessageView[];
  running: boolean; // a turn is streaming (drives the cancel button + composer)
}

export const EMPTY: ChatView = { messages: [], running: false };

// ── snapshot → initial state ─────────────────────────────────────────────────

/**
 * Build the initial view from a `chat.snapshot`: the finished turns from the transcript + forest,
 * then the running turn (if any) folded on top. The in-flight turn is NOT in the transcript yet — it
 * arrives as its raw material (`inflightInput` + `inflightEvents`) which we replay through the SAME
 * `applyEvent` as the live stream, so a reopen mid-turn and a live turn render identically (its tools
 * get live `l` keys, so a following live ToolEnd updates them in place). `now` seeds a still-open
 * call's live timer. A running turn always shows its assistant bubble even before its first event
 * streams (the Submit→turnStart window), so the composer state matches.
 */
export function seed(snap: ChatSnapshot, now: number): ChatView {
  let view: ChatView = { messages: buildSnapshotMessages(snap.messages, snap.tools ?? []), running: false };
  if (!snap.inflightRunning) return view;
  if (snap.inflightInput) view = pushUser(view, snap.inflightInput);
  for (const e of snap.inflightEvents ?? []) view = applyEvent(view, e, now);
  const last = view.messages[view.messages.length - 1];
  if (!last || last.role !== 'assistant' || !last.pending) {
    view = {
      ...view,
      messages: [...view.messages, { role: 'assistant', content: '', thinking: '', tools: [], pending: true }],
    };
  }
  return { ...view, running: true };
}

// ── live event → next state ──────────────────────────────────────────────────

/**
 * Fold one live event onto the view. Only frame-0 (top-level) events drive the visible answer bubble;
 * nested frames feed tool nesting via `applyTool`. `now` (ms epoch) seeds a starting call's live
 * timer — passed in so this stays pure and testable. Events the model does not render return the view
 * unchanged. Caller routes by chatId (event → active chat) before calling; that is transport routing,
 * not view state.
 */
export function applyEvent(view: ChatView, e: ServerEvent, now: number): ChatView {
  switch (e.type) {
    case 'chat.turnStart':
      // The assistant bubble is opened HERE, not inferred from the first token — so a locally sent
      // turn and a backend-initiated one (wake resume, agent run, another device) render identically.
      return e.frame === 0 ? openAssistant(view) : view;
    case 'chat.token':
      return e.frame === 0 ? appendAssistant(view, (m) => ({ ...m, content: m.content + e.text })) : view;
    case 'chat.thinking':
      return e.frame === 0 ? appendAssistant(view, (m) => ({ ...m, thinking: m.thinking + e.text })) : view;
    case 'chat.tool':
      return applyTool(view, e, now);
    case 'chat.turnEnd':
      if (e.frame !== 0) return view;
      return { ...appendAssistant(view, (m) => ({ ...m, error: e.err, pending: false })), running: false };
    default:
      return view;
  }
}

/** Optimistically echo the user's message as its bubble (running is set by the caller). */
export function pushUser(view: ChatView, content: string): ChatView {
  return { ...view, messages: [...view.messages, userBubble(content)] };
}

// ── bubble builders (shared by seed + the live fold) ─────────────────────────

function userBubble(content: string): ChatMessageView {
  return { role: 'user', content, thinking: '', tools: [], pending: false };
}

/** Open a fresh pending assistant bubble unless the last one already is one. */
function openAssistant(view: ChatView): ChatView {
  const ms = view.messages;
  const last = ms[ms.length - 1];
  if (last && last.role === 'assistant' && last.pending) return view;
  return { ...view, messages: [...ms, { role: 'assistant', content: '', thinking: '', tools: [], pending: true }] };
}

/** Apply `fn` to the last bubble iff it is the assistant turn currently streaming. */
function appendAssistant(view: ChatView, fn: (m: ChatMessageView) => ChatMessageView): ChatView {
  const ms = view.messages;
  if (!ms.length) return view;
  const i = ms.length - 1;
  if (ms[i].role !== 'assistant') return view;
  const next = ms.slice();
  next[i] = fn(next[i]);
  return { ...view, messages: next };
}

/** Match a tool start/end by id on the active assistant turn; nesting comes from the enclosing frame. */
function applyTool(view: ChatView, e: ChatTool, now: number): ChatView {
  return appendAssistant(view, (m) => {
    const parent = e.frame ? m.tools.find((t) => t.key === `l${e.frame}`) : undefined;
    const key = `l${e.id}`;
    const prev = m.tools.find((t) => t.key === key);
    const running = e.phase === 'start';
    const tv: ToolView = {
      key,
      tool: e.tool,
      args: e.args,
      result: e.result,
      err: e.err,
      running,
      depth: parent ? parent.depth + 1 : 0,
      id: e.id,
      parentId: e.frame || undefined, // enclosing call (0 = top-level); lets the parked branch reach ancestors
      // startedAt drives the live ticking timer WHILE running; on end the daemon's exact durationMs
      // freezes it (more accurate than a client clock, and it matches the reloaded snapshot).
      startedAt: prev?.startedAt ?? (running ? now : undefined),
      durationMs: running ? undefined : e.durationMs,
    };
    const idx = m.tools.findIndex((t) => t.key === key);
    const tools = idx >= 0 ? m.tools.map((t, i) => (i === idx ? tv : t)) : [...m.tools, tv];
    return { ...m, tools };
  });
}

// ── snapshot transcript → bubbles (pure, was chat-snapshot.ts) ───────────────

/**
 * Build snapshot messages from the persisted transcript + the per-turn tool forest. Turns are 1:1
 * with the transcript's user messages, so `forest[turn]` is the NESTED tool tree for that turn —
 * restoring the same depth/parent nesting the live stream shows, including nested host-bridge and
 * sub-agent calls the flat transcript loses. One assistant TURN spans several stored messages
 * (assistant(tool_calls) · tool(result) · assistant(text)) but is ONE bubble: consecutive assistant
 * messages merge, broken by a user message (which advances the turn index). Matches the live render.
 */
export function buildSnapshotMessages(messages: Message[], forest: ToolNode[][]): ChatMessageView[] {
  const out: ChatMessageView[] = [];
  let current: ChatMessageView | null = null;
  let turn = -1;
  for (const m of messages) {
    if (m.role === 'tool' || m.role === 'system') continue;
    if (m.role === 'user') {
      turn++;
      current = null;
      out.push(userBubble(m.content ?? ''));
      continue;
    }
    // A later assistant message of the same turn: merge its text. Its tools were already covered by
    // the turn's forest group (which spans every round of the turn).
    if (current) {
      if (m.content) current.content = current.content ? current.content + '\n' + m.content : m.content;
      continue;
    }
    // First assistant message of the turn: its tools are the whole turn's nested forest group.
    current = {
      role: 'assistant',
      content: m.content ?? '',
      thinking: '',
      tools: buildForestTools(forest[turn] ?? []),
      pending: false,
    };
    out.push(current);
  }
  return out;
}

/**
 * Build the rendered tool forest for a FINISHED turn from its persisted nodes: depth is the length of
 * the parent chain, id/parentId are restored so the render nests exactly like the live path. Nodes are
 * in start order, so parents precede children. Snapshot (`s`) keys, never running — a running turn's
 * tools come from the replayed live events (`applyEvent`), not from here.
 */
export function buildForestTools(nodes: ToolNode[]): ToolView[] {
  const byId = new Map<number, ToolNode>(nodes.map((n) => [n.id, n]));
  const depthOf = (n: ToolNode): number => {
    let d = 0;
    let p = n.parent;
    const seen = new Set<number>(); // guard against a malformed cycle
    while (p && !seen.has(p)) {
      seen.add(p);
      const parent = byId.get(p);
      if (!parent) break;
      d++;
      p = parent.parent;
    }
    return d;
  };
  return nodes.map((n) => ({
    key: `s${n.id}`,
    tool: n.tool,
    args: n.args,
    result: n.result,
    err: n.err,
    running: false,
    depth: depthOf(n),
    id: n.id,
    parentId: n.parent || undefined,
    durationMs: n.durationMs,
  }));
}
