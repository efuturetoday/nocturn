import type { Message, ToolNode } from '../protocol/nocturn-protocol';
import type { ChatMessageView, ToolView } from './chat-view';

/**
 * Pure transforms that rebuild a chat's rendered state from a `chat.snapshot`. No Angular, no signals
 * — just data in, view out, so they are unit-testable in isolation. The daemon always emits the
 * per-turn tool FOREST alongside the transcript, so there is no flat-tool fallback here.
 */

/**
 * Build snapshot messages from the persisted transcript + the per-turn tool forest. Turns are 1:1
 * with the transcript's user messages, so `forest[turn]` (turn = count of user messages seen) is the
 * NESTED tool tree for that turn — restoring the same depth/parent nesting the live stream shows,
 * including nested host-bridge and sub-agent calls the flat transcript loses.
 */
export function buildSnapshotMessages(messages: Message[], forest: ToolNode[][]): ChatMessageView[] {
  const out: ChatMessageView[] = [];
  // One assistant TURN spans several stored messages — assistant(tool_calls) · tool(result) ·
  // assistant(text) — but is ONE bubble. Merge consecutive assistant messages (tool/system skipped)
  // into the current bubble, broken by a user message (which also advances the turn index). This
  // matches the LIVE render (one bubble per turn).
  let current: ChatMessageView | null = null;
  let turn = -1;
  for (const m of messages) {
    if (m.role === 'tool' || m.role === 'system') continue;
    if (m.role === 'user') {
      turn++;
      current = null;
      out.push({ role: 'user', content: m.content ?? '', thinking: '', tools: [], pending: false });
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
 * Build the rendered tool forest from captured nodes: depth is the length of the parent chain (walked
 * within the group), and id/parentId are restored so the render nests exactly like the live path
 * (message-bubble indents by depth; parkedToolIds walks parentId). Nodes are in start order, so parents
 * precede children. `live=true` keys them in the live `l` namespace and honours each node's `running`
 * flag, so a still-open call of the in-flight turn shows as running and a following live ToolEnd
 * updates the SAME entry; a finished-turn forest uses `s` keys and is never running.
 */
export function buildForestTools(nodes: Array<ToolNode & { running?: boolean }>, live = false): ToolView[] {
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
    key: `${live ? 'l' : 's'}${n.id}`,
    tool: n.tool,
    args: n.args,
    result: n.result,
    err: n.err,
    running: live && !!n.running,
    depth: depthOf(n),
    id: n.id,
    parentId: n.parent || undefined,
    durationMs: n.durationMs,
  }));
}
