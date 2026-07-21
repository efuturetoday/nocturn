import { Injectable, inject, signal, computed, effect, untracked } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import type { ChatMeta, Message, ChatTool, ToolNode } from '../protocol/nocturn-protocol';

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

export interface PendingApproval {
  frame?: number; // the tool call this approval is for — its tool-frame freezes its timer
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

  // Read-state: the daemon owns a shared read cursor per chat (ChatMeta.read), advanced by markRead
  // and pushed to every device via chat.activity. A chat is unread when its `updated` is later.
  private readonly _viewing = signal<string | null>(null);

  /** Ids of chats with unread activity (updated > read). */
  readonly unreadIds = computed(() => {
    const out = new Set<string>();
    for (const c of this._chats()) {
      if (isUnread(c)) out.add(c.id);
    }
    return out;
  });

  /** True once we have an active chat. */
  readonly ready = computed(() => this._activeChatId() !== null);

  /**
   * The ids of every tool call frozen by the open approval: the exact tool the daemon named
   * (pendingApproval.frame) PLUS its ancestors, walked up the parentId chain. While an approval is
   * open the whole waiting branch is parked (the innermost tool waits; each ancestor is suspended on
   * it), so their timers must freeze — but a PARALLEL sibling branch, still executing, keeps ticking.
   */
  readonly parkedToolIds = computed(() => {
    const p = this._pendingApproval();
    const out = new Set<number>();
    if (p?.frame == null) return out;
    const byId = new Map<number, ToolView>();
    for (const m of this._messages()) for (const t of m.tools) if (t.id != null) byId.set(t.id, t);
    let id: number | undefined = p.frame;
    while (id != null && !out.has(id)) {
      out.add(id);
      id = byId.get(id)?.parentId;
    }
    return out;
  });

  /** Unread counts split by chat kind (agent runs badge the Agents tab, not Chat). */
  private readonly agentChatIds = computed(() => new Set(this._chats().filter((c) => c.source === 'agent').map((c) => c.id)));
  readonly unreadUserCount = computed(() => [...this.unreadIds()].filter((id) => !this.agentChatIds().has(id)).length);
  readonly unreadAgentCount = computed(() => [...this.unreadIds()].filter((id) => this.agentChatIds().has(id)).length);

  constructor() {
    this.conn.onEvent((e) => this.reduce(e));

    // Resync on (re)connect or active-workspace change: re-list + re-open → fresh snapshot. Only
    // once the active workspace is one the daemon actually serves — else a stale persisted name
    // would target an unknown workspace before workspace.list reconciles it.
    effect(() => {
      if (this.conn.state() !== 'connected') return;
      const ws = this.ws.active();
      if (!ws || !this.ws.workspaces().some((w) => w.name === ws)) return;
      // Read the active chat id UNTRACKED: this effect must re-run only on (re)connect / ws change,
      // NOT when a new chat's id lands (that would fire a redundant chat.open every time it changes).
      const id = untracked(() => this._activeChatId());
      this.conn.send({ cmd: 'chat.list', ws });
      if (id) this.conn.send({ cmd: 'chat.open', ws, id });
    });
  }

  // ── commands (ws = the app-wide active workspace) ────────────────────────────

  listChats(): void {
    const ws = this.ws.active();
    if (ws) this.conn.send({ cmd: 'chat.list', ws });
  }

  /** Begin a fresh chat: MINT its id client-side and make it active — the first submit creates it on
      the daemon (an unknown id starts that chat). Returns the id so the caller can navigate to it. */
  newChat(): string {
    const id = newChatId();
    this._activeChatId.set(id);
    this.resetLocal();
    return id;
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

  /** Send a message: optimistically echo it as the user bubble, then stream the reply. The assistant
      bubble is NOT opened here — it emerges from the event stream (the first frame-0 event opens it),
      so a locally-sent turn and a backend-initiated one (wake resume, agent run, another device)
      render identically. The chat is addressed by its client-minted id (always set). */
  submit(input: string): void {
    const text = input.trim();
    const ws = this.ws.active();
    const id = this._activeChatId();
    if (!text || !ws || !id) return;
    this.pushUser(text);
    this._running.set(true);
    this.conn.send({ cmd: 'chat.submit', ws, text, id });
  }

  cancel(): void {
    const ws = this.ws.active();
    const id = this._activeChatId();
    if (ws && id) this.conn.send({ cmd: 'chat.cancel', ws, id });
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
        if (e.ws === this.ws.active()) this._chats.set(e.chats);
        break;

      case 'chat.activity':
        if (e.ws === this.ws.active()) {
          this.upsertChat(e.chat);
          // Keep the on-screen chat read as its turns stream in — but ONLY when it is actually unread
          // (updated > read). markRead itself broadcasts a chat.activity (with read == updated); without
          // this guard that echo would re-trigger markRead forever, a tight client↔daemon loop.
          if (e.chat.id === this._viewing() && isUnread(e.chat)) this.markRead(e.chat.id);
        }
        break;

      case 'chat.snapshot': {
        this._activeChatId.set(e.id);
        const msgs = this.buildSnapshotMessages(e.messages, e.tools ?? []);
        // The in-flight turn is NOT in the transcript yet — append it so a reopen mid-turn shows the
        // user's own message + a pending assistant (partial answer/reasoning + running forest). Live
        // events then stream onto this same pending bubble (its tools use live `l` keys, so a following
        // ToolEnd updates them in place). Without this the running turn would vanish on reopen.
        const inf = e.inflight;
        if (inf?.running) {
          if (inf.input) msgs.push({ role: 'user', content: inf.input, thinking: '', tools: [], pending: false });
          msgs.push({
            role: 'assistant',
            content: inf.answer ?? '',
            thinking: inf.thinking ?? '',
            tools: buildForestTools(inf.tools ?? [], true),
            pending: true,
          });
        }
        this._messages.set(msgs);
        this._running.set(!!inf?.running);
        break;
      }

      // Streaming events broadcast for EVERY live chat; apply only those for the chat on screen.
      // The assistant bubble is opened by chat.turnStart (below), NOT inferred here — so a locally
      // sent turn and a backend-initiated one (wake resume, agent run, another device) render the
      // same, straight from the stream.
      case 'chat.turnStart':
        if (e.chatId !== this._activeChatId()) break;
        if (e.frame === 0) this.openAssistant();
        break;

      case 'chat.token':
        if (e.chatId !== this._activeChatId()) break;
        if (e.frame === 0) this.appendAssistant((m) => ({ ...m, content: m.content + e.text }));
        break;

      case 'chat.thinking':
        if (e.chatId !== this._activeChatId()) break;
        if (e.frame === 0) this.appendAssistant((m) => ({ ...m, thinking: m.thinking + e.text }));
        break;

      case 'chat.tool':
        if (e.chatId !== this._activeChatId()) break;
        this.applyTool(e);
        break;

      case 'chat.turnEnd':
        if (e.chatId !== this._activeChatId()) break;
        if (e.frame === 0) {
          this.appendAssistant((m) => ({ ...m, error: e.err, pending: false }));
          this._running.set(false);
          // The unread dot updates from the daemon's chat.activity push, not from here.
        }
        break;

      case 'approval.request':
        this._pendingApproval.set({ id: e.id, frame: e.frame, intent: e.intent, options: e.options });
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
    this.markRead(id);
  }

  stopViewing(id: string): void {
    if (this._viewing() === id) this._viewing.set(null);
  }

  /** Advance a chat's shared read cursor on the daemon (clears its dot on every device). */
  private markRead(id: string): void {
    const ws = this.ws.active();
    if (ws) this.conn.send({ cmd: 'chat.markRead', ws, id });
  }

  /** Insert or replace one chat's metadata in the list (from a chat.activity push). */
  private upsertChat(c: ChatMeta): void {
    this._chats.update((cs) => {
      const i = cs.findIndex((x) => x.id === c.id);
      if (i >= 0) {
        const next = cs.slice();
        next[i] = c;
        return next;
      }
      return [c, ...cs];
    });
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
    const now = Date.now();
    this.appendAssistant((m) => {
      const parent = e.frame ? m.tools.find((t) => t.key === `l${e.frame}`) : undefined;
      const key = `l${e.id}`;
      const prev = m.tools.find((t) => t.key === key);
      const running = e.phase === 'start';
      const view: ToolView = {
        key,
        tool: e.tool,
        args: e.args,
        result: e.result,
        err: e.err,
        running,
        depth: parent ? parent.depth + 1 : 0,
        id: e.id,
        parentId: e.frame || undefined, // the enclosing call (0 = top-level); lets the parked branch reach its ancestors

        // startedAt drives the live ticking timer WHILE running; on end the daemon's exact durationMs
        // freezes it (more accurate than a client clock, and it matches the reloaded snapshot).
        startedAt: prev?.startedAt ?? (running ? now : undefined),
        durationMs: running ? undefined : e.durationMs,
      };
      const idx = m.tools.findIndex((t) => t.key === key);
      const tools = idx >= 0 ? m.tools.map((t, i) => (i === idx ? view : t)) : [...m.tools, view];
      return { ...m, tools };
    });
  }

  /**
   * Build snapshot messages from the persisted transcript + the per-turn tool forest. Turns are 1:1
   * with the transcript's user messages, so `forest[turn]` (turn = count of user messages seen) is the
   * NESTED tool tree for that turn — restoring the same depth/parent nesting the live stream shows,
   * including nested host-bridge and sub-agent calls the flat transcript loses. When a turn has no
   * forest group (an old transcript from before this was captured), it falls back to the flat
   * `toolCalls` carried on the messages, with results/durations folded in by toolCallID.
   */
  private buildSnapshotMessages(messages: Message[], forest: ToolNode[][]): ChatMessageView[] {
    const results = new Map<string, string>();
    const durations = new Map<string, number>();
    for (const m of messages) {
      if (m.role === 'tool' && m.toolCallID) {
        results.set(m.toolCallID, m.content ?? '');
        if (m.durationMs != null) durations.set(m.toolCallID, m.durationMs);
      }
    }
    const out: ChatMessageView[] = [];
    // One assistant TURN spans several stored messages — assistant(tool_calls) · tool(result) ·
    // assistant(text) — but is ONE bubble. Merge consecutive assistant messages (tool/system skipped)
    // into the current bubble, broken by a user message (which also advances the turn index). This
    // matches the LIVE render (one bubble per turn).
    let current: ChatMessageView | null = null;
    let turn = -1;
    let usedForest = false; // this turn's tools came from the forest group (whole turn) — don't also fold flat
    for (const m of messages) {
      if (m.role === 'tool' || m.role === 'system') continue;
      if (m.role === 'user') {
        turn++;
        current = null;
        out.push({ role: 'user', content: m.content ?? '', thinking: '', tools: [], pending: false });
        continue;
      }
      if (current) {
        // A later assistant message of the same turn: merge its text. Its tools were already covered by
        // the turn's forest group (which spans all rounds); only the legacy path folds per-message.
        if (m.content) current.content = current.content ? current.content + '\n' + m.content : m.content;
        if (!usedForest) current.tools = [...current.tools, ...this.flatTools(m, results, durations)];
        continue;
      }
      // First assistant message of the turn: prefer the nested forest group; else the flat fallback.
      const group = forest[turn];
      usedForest = !!group?.length;
      const tools = usedForest ? buildForestTools(group) : this.flatTools(m, results, durations);
      current = { role: 'assistant', content: m.content ?? '', thinking: '', tools, pending: false };
      out.push(current);
    }
    return out;
  }

  /** Flat tools from one assistant message's toolCalls (legacy path — no nesting), results/durations
   * folded in by toolCallID. */
  private flatTools(m: Message, results: Map<string, string>, durations: Map<string, number>): ToolView[] {
    return (m.toolCalls ?? []).map((tc) => ({
      key: `s${tc.id}`,
      tool: tc.tool,
      args: tc.args,
      result: results.get(tc.id),
      running: false,
      depth: 0,
      durationMs: durations.get(tc.id),
    }));
  }
}

/** Build the rendered tool forest from captured nodes: depth is the length of the parent chain (walked
 * within the group), and id/parentId are restored so the render nests exactly like the live path
 * (message-bubble indents by depth; parkedToolIds walks parentId). Nodes are in start order, so parents
 * precede children. `live=true` keys them in the live `l` namespace and honours each node's `running`
 * flag, so a still-open call of the in-flight turn shows as running and a following live ToolEnd
 * updates the SAME entry; a finished-turn forest uses `s` keys and is never running. */
function buildForestTools(nodes: Array<ToolNode & { running?: boolean }>, live = false): ToolView[] {
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

/** A chat is unread when it has activity past its shared read cursor (or was never read). */
function isUnread(c: ChatMeta): boolean {
  return !c.read || new Date(c.updated) > new Date(c.read);
}

/** Mint a chat id client-side: 6 random bytes as lowercase hex (matches the daemon's format, which
 * validates it before use). Client-minted ids make a new-chat submit self-identifying — no
 * server round-trip to learn the id. */
function newChatId(): string {
  const b = new Uint8Array(6);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
}
