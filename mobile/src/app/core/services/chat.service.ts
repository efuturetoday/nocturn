import { Injectable, inject, signal, computed, effect } from '@angular/core';
import { ConnectionService } from './connection.service';
import type { ChatMeta, ToolFrame, SnapshotTool, ApprovalEvent } from '../protocol/nocturn-protocol';

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

  private readonly _chats = signal<ChatMeta[]>([]);
  readonly chats = this._chats.asReadonly();

  private readonly _activeWs = signal<string | null>(null);
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

  // Badge signals for chats the client does NOT have open (server ChatActivity pushes).
  private readonly _unread = signal<ReadonlySet<string>>(new Set());
  readonly unread = this._unread.asReadonly();
  private readonly _approvalWaiting = signal<ReadonlySet<string>>(new Set());
  readonly approvalWaiting = this._approvalWaiting.asReadonly();

  /** True once we've received a snapshot for the active chat. */
  readonly ready = computed(() => this._activeChatId() !== null);

  constructor() {
    this.conn.onEvent((e) => this.reduce(e));

    // Resync the active chat on (re)connect: re-list + re-open → fresh snapshot.
    effect(() => {
      if (this.conn.state() !== 'connected') return;
      const ws = this._activeWs();
      const id = this._activeChatId();
      if (ws) this.conn.send({ cmd: 'listChats', ws });
      if (ws && id) this.conn.send({ cmd: 'openChat', ws, id });
    });
  }

  // ── commands ───────────────────────────────────────────────────────────────

  listChats(ws: string): void {
    this._activeWs.set(ws);
    this.conn.send({ cmd: 'listChats', ws });
  }

  newChat(ws: string, name: string): void {
    this._activeWs.set(ws);
    this.conn.send({ cmd: 'newChat', ws, name });
  }

  /** Open a chat: clears local state + its unread badges, and requests its snapshot. */
  openChat(ws: string, id: string): void {
    this._activeWs.set(ws);
    this._activeChatId.set(id);
    this.clearBadge(id);
    this.resetLocal();
    this.conn.send({ cmd: 'openChat', ws, id });
  }

  renameChat(id: string, name: string): void {
    const ws = this._activeWs();
    if (ws) this.conn.send({ cmd: 'renameChat', ws, id, name });
  }

  deleteChat(id: string): void {
    const ws = this._activeWs();
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
        if (e.ws === this._activeWs()) this._chats.set(e.items);
        break;

      case 'snapshot': {
        this._running.set(e.running);
        this._messages.set(
          e.messages.map((m) => ({
            role: m.role,
            content: m.content,
            thinking: '',
            tools: (m.tools ?? []).map((t, i) => this.snapshotTool(t, i)),
            pending: false,
          })),
        );
        this._pendingApproval.set(e.pending ? this.toApproval(e.pending) : null);
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
        // Badge a chat we don't have open; the open chat streams its full events already.
        if (e.id !== this._activeChatId()) {
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
    const view = this.liveTool(frame);
    this.appendAssistant((m) => {
      const idx = m.tools.findIndex((t) => t.key === view.key);
      const tools = idx >= 0 ? m.tools.map((t, i) => (i === idx ? view : t)) : [...m.tools, view];
      return { ...m, tools };
    });
  }

  private liveTool(f: ToolFrame): ToolView {
    return { key: `l${f.id}`, tool: f.tool, args: f.args, result: f.result, err: f.err, running: f.phase === 'start' };
  }

  private snapshotTool(t: SnapshotTool, i: number): ToolView {
    return { key: `s${i}`, tool: t.tool, args: t.args, result: t.result, running: false };
  }
}
