import { Injectable, inject, signal, computed, effect } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import type { ChatMeta, ToolFrame, SnapshotTool, ForestFrame, Message, ApprovalEvent } from '../protocol/nocturn-protocol';

/**
 * A rendered tool call — unifies the live `ToolFrame` (streamed, has phase/err) and the
 * static `SnapshotTool` (finished, from history) so the UI renders one shape.
 */
export interface ToolView {
  key: string; // stable track id
  tool: string;
  args?: string;
  result?: string;
  err?: string;
  running: boolean;
  depth: number; // nesting: 0 = top-level model call, >0 = sub-call (indent)
}

/** A rendered conversation message: user text, or an assistant turn with reasoning + tools. */
export interface ChatMessageView {
  role: 'user' | 'assistant';
  content: string;
  thinking: string; // dim reasoning, assistant only
  tools: ToolView[]; // the assistant turn's tool calls (live or from snapshot)
  error?: string;
  pending: boolean; // assistant turn still streaming
}

export interface PendingApproval {
  id: string;
  intent: string;
  options: string[];
}

/**
 * ChatService owns the chat list per workspace and the ONE active chat's assembled state. It
 * consumes the chat-stream events (`snapshot`/`token`/`thinking`/`tool`/`turn*`/`approval`…)
 * and reduces them into `messages` + `toolForest` + `pendingApproval`. `snapshot` is the
 * wholesale resync primitive; on (re)connect it re-opens the active chat to pull a fresh one.
 */
@Injectable({ providedIn: 'root' })
export class ChatService {
  private readonly conn = inject(ConnectionService);
  private readonly ws = inject(WorkspaceService);

  private readonly _chats = signal<ChatMeta[]>([]);
  readonly chats = this._chats.asReadonly();

  private readonly _activeChatId = signal<string | null>(null);
  readonly activeChatId = this._activeChatId.asReadonly();

  private readonly _messages = signal<ChatMessageView[]>([]);
  readonly messages = this._messages.asReadonly();

  private readonly _running = signal(false);
  readonly running = this._running.asReadonly();

  private readonly _pendingApproval = signal<PendingApproval | null>(null);
  readonly pendingApproval = this._pendingApproval.asReadonly();

  private readonly _notice = signal<string | null>(null);
  readonly notice = this._notice.asReadonly();

  // First message to auto-send once a freshly-created chat opens (Gemini-style composer).
  private readonly pendingFirst = signal<string | null>(null);

  // Badge signals for chats the client does NOT have open (server ChatActivity pushes).
  private readonly _unread = signal<ReadonlySet<string>>(new Set());
  readonly unread = this._unread.asReadonly();
  private readonly _approvalWaiting = signal<ReadonlySet<string>>(new Set());
  readonly approvalWaiting = this._approvalWaiting.asReadonly();

  /** True once we've received a snapshot for the active chat. */
  readonly ready = computed(() => this._activeChatId() !== null);

  /** Unread counts split by chat kind (agent runs badge the Agents tab, not Chat). */
  private readonly agentChatIds = computed(() => new Set(this._chats().filter((c) => c.agent).map((c) => c.id)));
  readonly unreadUserCount = computed(() => [...this._unread()].filter((id) => !this.agentChatIds().has(id)).length);
  readonly unreadAgentCount = computed(() => [...this._unread()].filter((id) => this.agentChatIds().has(id)).length);

  constructor() {
    this.conn.onEvent((e) => this.reduce(e));

    // Resync on (re)connect OR when the active workspace changes: re-list + re-open → fresh
    // snapshot. Depends on WorkspaceService.active() so switching workspace reloads its chats.
    effect(() => {
      if (this.conn.state() !== 'connected') return;
      const ws = this.ws.active();
      const id = this._activeChatId();
      if (ws) this.conn.send({ cmd: 'listChats', ws });
      if (ws && id) this.conn.send({ cmd: 'openChat', ws, id });
    });
  }

  // ── commands (ws is the app-wide active workspace) ───────────────────────────

  listChats(): void {
    const ws = this.ws.active();
    if (ws) this.conn.send({ cmd: 'listChats', ws });
  }

  newChat(name: string): void {
    const ws = this.ws.active();
    if (ws) this.conn.send({ cmd: 'newChat', ws, name });
  }

  /** Queue a first message to auto-send when the next freshly-opened (empty) chat snapshots. */
  queueFirstMessage(text: string): void {
    this.pendingFirst.set(text.trim() || null);
  }

  /** Open a chat: clears local state + its unread badges, and requests its snapshot. */
  openChat(id: string): void {
    const ws = this.ws.active();
    if (!ws) return;
    this._activeChatId.set(id);
    this.clearBadge(id);
    this.resetLocal();
    this.conn.send({ cmd: 'openChat', ws, id });
  }

  renameChat(id: string, name: string): void {
    const ws = this.ws.active();
    if (ws) this.conn.send({ cmd: 'renameChat', ws, id, name });
  }

  deleteChat(id: string): void {
    const ws = this.ws.active();
    if (ws) this.conn.send({ cmd: 'deleteChat', ws, id });
  }

  submit(input: string): void {
    const text = input.trim();
    if (text) this.conn.send({ cmd: 'submit', input: text });
  }

  cancel(): void {
    this.conn.send({ cmd: 'cancel' });
  }

  reset(): void {
    this.conn.send({ cmd: 'reset' });
  }

  /** Answer the pending approval by option index (-1 = deny). */
  resolve(choice: number): void {
    const p = this._pendingApproval();
    if (!p) return;
    this.conn.send({ cmd: 'resolve', id: p.id, choice });
    this._pendingApproval.set(null);
  }

  // ── event reduction ──────────────────────────────────────────────────────────

  private reduce(e: import('../protocol/nocturn-protocol').ServerEvent): void {
    switch (e.type) {
      case 'chats':
        if (e.ws === this.ws.active()) this._chats.set(e.items);
        break;

      case 'snapshot': {
        this._running.set(e.running);
        this._messages.set(this.buildSnapshotMessages(e.messages, e.forest));
        this._pendingApproval.set(e.pending ? this.toApproval(e.pending) : null);
        // Fresh chat just opened — fire the queued first message (composer flow).
        const first = this.pendingFirst();
        if (first && e.messages.length === 0) {
          this.pendingFirst.set(null);
          this.submit(first);
        }
        break;
      }

      case 'turnStart':
        if (e.source === 'user') this.pushUser(e.display);
        this.openAssistant();
        this._running.set(true);
        break;

      case 'token':
        this.appendAssistant((m) => ({ ...m, content: m.content + e.text }));
        break;

      case 'thinking':
        this.appendAssistant((m) => ({ ...m, thinking: m.thinking + e.text }));
        break;

      case 'tool':
        this.applyTool(e.tool);
        break;

      case 'turnEnd':
        this.appendAssistant((m) => ({
          ...m,
          content: e.answer && !m.content ? e.answer : m.content,
          error: e.err,
          pending: false,
        }));
        this._running.set(false);
        break;

      case 'queued':
        this._notice.set(`queued: ${e.display}`);
        break;

      case 'notice':
        this._notice.set(e.text);
        break;

      case 'approval':
        this._pendingApproval.set(this.toApproval(e));
        break;

      case 'approvalResolved':
        if (this._pendingApproval()?.id === e.id) this._pendingApproval.set(null);
        break;

      case 'chatActivity':
        // Badge a chat in the active workspace we don't have open; the open chat streams its
        // full events already.
        if (e.ws === this.ws.active() && e.id !== this._activeChatId()) {
          this._unread.update((s) => new Set(s).add(e.id));
          if (e.kind === 'approvalPending') {
            this._approvalWaiting.update((s) => new Set(s).add(e.id));
          }
        }
        break;
    }
  }

  private clearBadge(id: string): void {
    this._unread.update((s) => {
      if (!s.has(id)) return s;
      const next = new Set(s);
      next.delete(id);
      return next;
    });
    this._approvalWaiting.update((s) => {
      if (!s.has(id)) return s;
      const next = new Set(s);
      next.delete(id);
      return next;
    });
  }

  private toApproval(e: ApprovalEvent): PendingApproval {
    return { id: e.id, intent: e.intent, options: e.options };
  }

  private resetLocal(): void {
    this._messages.set([]);
    this._running.set(false);
    this._pendingApproval.set(null);
  }

  private pushUser(content: string): void {
    this._messages.update((ms) => [...ms, { role: 'user', content, thinking: '', tools: [], pending: false }]);
  }

  /** Ensure there's a trailing in-progress assistant message to stream into. */
  private openAssistant(): void {
    this._messages.update((ms) => {
      const last = ms[ms.length - 1];
      if (last && last.role === 'assistant' && last.pending) return ms;
      return [...ms, { role: 'assistant', content: '', thinking: '', tools: [], pending: true }];
    });
  }

  private appendAssistant(fn: (m: ChatMessageView) => ChatMessageView): void {
    this._messages.update((ms) => {
      if (!ms.length) return ms;
      const i = ms.length - 1;
      if (ms[i].role !== 'assistant') return ms;
      const next = ms.slice();
      next[i] = fn(next[i]);
      return next;
    });
  }

  /** Match tool start/end by id on the active assistant turn; flip start→end in place. */
  private applyTool(frame: ToolFrame): void {
    this.appendAssistant((m) => {
      // Depth from the parent already in this turn's tools (nested effects indent under it).
      const parent = frame.parent ? m.tools.find((t) => t.key === `l${frame.parent}`) : undefined;
      const view: ToolView = {
        key: `l${frame.id}`,
        tool: frame.tool,
        args: frame.args,
        result: frame.result,
        err: frame.err,
        running: frame.phase === 'start',
        depth: parent ? parent.depth + 1 : 0,
      };
      const idx = m.tools.findIndex((t) => t.key === view.key);
      const tools = idx >= 0 ? m.tools.map((t, i) => (i === idx ? view : t)) : [...m.tools, view];
      return { ...m, tools };
    });
  }

  /**
   * Build snapshot messages, attaching each assistant turn its tool tree. Prefers the full
   * `forest` (id/parent → sub-calls + errors); consumes its top-level frames in order, one
   * group per top-level call recorded in that turn's `Message.tools`. Falls back to the flat
   * `Message.tools` when no forest is present.
   */
  private buildSnapshotMessages(messages: Message[], forest?: ForestFrame[]): ChatMessageView[] {
    const children = new Map<number, ForestFrame[]>();
    const topLevel: ForestFrame[] = [];
    for (const f of forest ?? []) {
      if (f.parent === 0) topLevel.push(f);
      else (children.get(f.parent) ?? children.set(f.parent, []).get(f.parent)!).push(f);
    }
    let ti = 0; // pointer into topLevel, consumed across assistant turns in stream order

    const flatten = (f: ForestFrame, depth: number, out: ToolView[]): void => {
      out.push({ key: `f${f.id}`, tool: f.tool, args: f.args, result: f.result, err: f.err, running: false, depth });
      for (const c of children.get(f.id) ?? []) flatten(c, depth + 1, out);
    };

    return messages.map((m) => {
      let tools: ToolView[] = [];
      if (m.role === 'assistant') {
        const k = m.tools?.length ?? 0;
        if (forest && forest.length) {
          const out: ToolView[] = [];
          for (let n = 0; n < k && ti < topLevel.length; n++, ti++) flatten(topLevel[ti], 0, out);
          tools = out;
        } else {
          tools = (m.tools ?? []).map((t, i) => this.snapshotTool(t, i));
        }
      }
      return { role: m.role, content: m.content, thinking: '', tools, pending: false };
    });
  }

  private snapshotTool(t: SnapshotTool, i: number): ToolView {
    return { key: `s${i}`, tool: t.tool, args: t.args, result: t.result, running: false, depth: 0 };
  }
}
