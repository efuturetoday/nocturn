import { Injectable, inject, signal, computed, effect } from '@angular/core';
import { Preferences } from '@capacitor/preferences';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import type { ChatMeta, Message, ChatTool, ApprovalRequest } from '../protocol/nocturn-protocol';

/** A rendered tool call — live (streamed, has phase/err) or from a snapshot (finished). */
export interface ToolView {
  key: string; // stable track id
  tool: string;
  args?: string;
  result?: string;
  err?: string;
  running: boolean;
  depth: number; // nesting: 0 = top-level call, >0 = sub-agent call (indent)
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

export interface PendingApproval {
  id: string;
  intent: string;
  options: string[];
}

/**
 * ChatService owns the chat list per workspace and the ONE active chat's assembled state. It reduces
 * the chat.* stream (snapshot/token/thinking/tool/turnEnd) + approval.* into `messages` +
 * `pendingApproval`. A chat is message-first: submitting with no active chat starts one, and the
 * daemon replies chat.opened with its id. `chat.snapshot` is the wholesale resync primitive; on
 * (re)connect it re-opens the active chat to pull a fresh one.
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

  // First message to auto-send once the composer opens a fresh chat (consumed by the chat page).
  private readonly _pendingFirst = signal<string | null>(null);

  // Kept for template compatibility; the per-chat approval badge needs a backend signal we don't have
  // yet, so it stays empty (a pending approval surfaces inline on the open chat instead).
  private readonly _approvalWaiting = signal<ReadonlySet<string>>(new Set());
  readonly approvalWaiting = this._approvalWaiting.asReadonly();

  // Read-state: local optimistic seen-times (no shared cross-device cursor yet). A chat is unread
  // when its `updated` is newer than what this device has seen.
  private readonly _seen = signal<Record<string, string>>({});
  private readonly _viewing = signal<string | null>(null);

  /** Ids of chats with unread activity (updated > seen). */
  readonly unreadIds = computed(() => {
    const seen = this._seen();
    const out = new Set<string>();
    for (const c of this._chats()) {
      const at = seen[c.id];
      if (!at || new Date(c.updated) > new Date(at)) out.add(c.id);
    }
    return out;
  });

  /** True once we have an active chat. */
  readonly ready = computed(() => this._activeChatId() !== null);

  /** Unread counts split by chat kind (agent runs badge the Agents tab, not Chat). */
  private readonly agentChatIds = computed(() => new Set(this._chats().filter((c) => c.source === 'agent').map((c) => c.id)));
  readonly unreadUserCount = computed(() => [...this.unreadIds()].filter((id) => !this.agentChatIds().has(id)).length);
  readonly unreadAgentCount = computed(() => [...this.unreadIds()].filter((id) => this.agentChatIds().has(id)).length);

  constructor() {
    void this.loadSeen();
    this.conn.onEvent((e) => this.reduce(e));

    // Resync on (re)connect or active-workspace change: re-list + re-open → fresh snapshot.
    effect(() => {
      if (this.conn.state() !== 'connected') return;
      const ws = this.ws.active();
      const id = this._activeChatId();
      if (ws) this.conn.send({ cmd: 'chat.list', ws });
      if (ws && id) this.conn.send({ cmd: 'chat.open', ws, id });
    });
  }

  // ── commands (ws = the app-wide active workspace) ────────────────────────────

  listChats(): void {
    const ws = this.ws.active();
    if (ws) this.conn.send({ cmd: 'chat.list', ws });
  }

  /** Begin a fresh chat: clear the active one so the next submit starts a new chat (message-first). */
  newChat(): void {
    this._activeChatId.set(null);
    this.resetLocal();
  }

  /** Queue a first message the chat page submits once its composer is ready. */
  queueFirstMessage(text: string): void {
    this._pendingFirst.set(text.trim() || null);
  }

  /** Take (and clear) the queued first message, if any. */
  takePendingFirst(): string | null {
    const v = this._pendingFirst();
    this._pendingFirst.set(null);
    return v;
  }

  /** Open a chat: clear local state + its unread badge, request its snapshot. */
  openChat(id: string): void {
    const ws = this.ws.active();
    if (!ws) return;
    this._activeChatId.set(id);
    this.clearBadge(id);
    this.resetLocal();
    this.conn.send({ cmd: 'chat.open', ws, id });
  }

  /** Send a message: optimistically show it + open an assistant bubble, then stream the reply. */
  submit(input: string): void {
    const text = input.trim();
    const ws = this.ws.active();
    if (!text || !ws) return;
    this.pushUser(text);
    this.openAssistant();
    this._running.set(true);
    this.conn.send({ cmd: 'chat.submit', ws, text });
  }

  cancel(): void {
    this.conn.send({ cmd: 'chat.cancel' });
  }

  /** Answer the pending approval by option index (-1 = deny). */
  resolve(choice: number): void {
    const p = this._pendingApproval();
    if (!p) return;
    this.conn.send({ cmd: 'approval.resolve', id: p.id, choice });
    this._pendingApproval.set(null);
  }

  // ── event reduction ──────────────────────────────────────────────────────────

  private reduce(e: import('../protocol/nocturn-protocol').ServerEvent): void {
    switch (e.type) {
      case 'chat.list':
        if (e.ws === this.ws.active()) {
          this._chats.set(e.chats);
          const v = this._viewing();
          const cur = v ? e.chats.find((c) => c.id === v) : undefined;
          if (cur) this.markSeen(cur.id, cur.updated);
        }
        break;

      case 'chat.opened':
        this._activeChatId.set(e.id);
        break;

      case 'chat.snapshot':
        this._activeChatId.set(e.id);
        this._messages.set(this.buildSnapshotMessages(e.messages));
        this._running.set(false);
        break;

      case 'chat.token':
        if (e.frame === 0) this.appendAssistant((m) => ({ ...m, content: m.content + e.text }));
        break;

      case 'chat.thinking':
        if (e.frame === 0) this.appendAssistant((m) => ({ ...m, thinking: m.thinking + e.text }));
        break;

      case 'chat.tool':
        this.applyTool(e);
        break;

      case 'chat.turnEnd':
        if (e.frame === 0) {
          this.appendAssistant((m) => ({ ...m, error: e.err, pending: false }));
          this._running.set(false);
        }
        break;

      case 'approval.request':
        this._pendingApproval.set({ id: e.id, intent: e.intent, options: e.options });
        break;

      case 'approval.resolved':
        if (this._pendingApproval()?.id === e.id) this._pendingApproval.set(null);
        break;
    }
  }

  // ── read-state (timestamp unread, local) ─────────────────────────────────────

  startViewing(id: string): void {
    this._viewing.set(id);
    this.clearBadge(id);
    const cur = this._chats().find((c) => c.id === id);
    if (cur) this.markSeen(id, cur.updated);
  }

  stopViewing(id: string): void {
    if (this._viewing() === id) this._viewing.set(null);
  }

  private markSeen(id: string, updated: string): void {
    if (!updated || this._seen()[id] === updated) return;
    const next = { ...this._seen(), [id]: updated };
    this._seen.set(next);
    void Preferences.set({ key: 'nocturn.seen', value: JSON.stringify(next) });
  }

  private async loadSeen(): Promise<void> {
    const { value } = await Preferences.get({ key: 'nocturn.seen' });
    if (value) {
      try {
        this._seen.set(JSON.parse(value) as Record<string, string>);
      } catch {
        /* ignore corrupt cache */
      }
    }
  }

  private clearBadge(id: string): void {
    this._approvalWaiting.update((s) => {
      if (!s.has(id)) return s;
      const next = new Set(s);
      next.delete(id);
      return next;
    });
  }

  private resetLocal(): void {
    this._messages.set([]);
    this._running.set(false);
    this._pendingApproval.set(null);
  }

  private pushUser(content: string): void {
    this._messages.update((ms) => [...ms, { role: 'user', content, thinking: '', tools: [], pending: false }]);
  }

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

  /** Match tool start/end by id on the active assistant turn; nesting comes from the enclosing frame. */
  private applyTool(e: ChatTool): void {
    this.appendAssistant((m) => {
      const parent = e.frame ? m.tools.find((t) => t.key === `l${e.frame}`) : undefined;
      const view: ToolView = {
        key: `l${e.id}`,
        tool: e.tool,
        args: e.args,
        result: e.result,
        err: e.err,
        running: e.phase === 'start',
        depth: parent ? parent.depth + 1 : 0,
      };
      const idx = m.tools.findIndex((t) => t.key === view.key);
      const tools = idx >= 0 ? m.tools.map((t, i) => (i === idx ? view : t)) : [...m.tools, view];
      return { ...m, tools };
    });
  }

  /**
   * Build snapshot messages from the persisted transcript. An assistant message's tool calls carry
   * their args; the matching tool-result messages (linked by toolCallID) carry the results, folded
   * back onto each call. Tool-result and system messages are not shown on their own.
   */
  private buildSnapshotMessages(messages: Message[]): ChatMessageView[] {
    const results = new Map<string, string>();
    for (const m of messages) {
      if (m.role === 'tool' && m.toolCallID) results.set(m.toolCallID, m.content ?? '');
    }
    const out: ChatMessageView[] = [];
    for (const m of messages) {
      if (m.role === 'tool' || m.role === 'system') continue;
      const tools: ToolView[] = (m.toolCalls ?? []).map((tc) => ({
        key: `s${tc.id}`,
        tool: tc.tool,
        args: tc.args,
        result: results.get(tc.id),
        running: false,
        depth: 0,
      }));
      out.push({ role: m.role, content: m.content ?? '', thinking: '', tools, pending: false });
    }
    return out;
  }
}
