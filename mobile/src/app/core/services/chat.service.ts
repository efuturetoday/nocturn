import { Injectable, inject, signal, computed, effect, untracked } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import { ApprovalService } from './approval.service';
import type { ChatTool, ServerEvent } from '../protocol/nocturn-protocol';
import type { ChatMessageView, ToolView } from './chat-view';
import { buildForestTools, buildSnapshotMessages } from './chat-snapshot';

/**
 * ChatService owns the ONE active chat's assembled state. It reduces that chat's stream
 * (snapshot/token/thinking/tool/turnEnd) into `messages`. A chat is message-first: submitting with no
 * active chat starts one (an unknown client-minted id creates it on the daemon). `chat.snapshot` is
 * the wholesale resync primitive; on (re)connect it re-opens the active chat to pull a fresh one. The
 * chat LIST + unread state live in ChatListService; the app-global out-of-band approval state lives in
 * ApprovalService (an approval carries no chat id) — this service only reads its `frames()` to freeze
 * the parked branch's timers.
 */
@Injectable({ providedIn: 'root' })
export class ChatService {
  private readonly conn = inject(ConnectionService);
  private readonly ws = inject(WorkspaceService);
  private readonly approvals = inject(ApprovalService);

  private readonly _activeChatId = signal<string | null>(null);
  readonly activeChatId = this._activeChatId.asReadonly();

  private readonly _messages = signal<ChatMessageView[]>([]);
  readonly messages = this._messages.asReadonly();

  private readonly _running = signal(false);
  readonly running = this._running.asReadonly();

  // First message to auto-send once the composer opens a fresh chat (consumed by the chat page).
  private readonly _pendingFirst = signal<string | null>(null);

  /**
   * The ids of every tool call frozen by an open approval: each approval's named tool frame PLUS its
   * ancestors, walked up the parentId chain in THIS chat's tool map. While an approval is open the
   * whole waiting branch is parked (the innermost tool waits; each ancestor is suspended on it), so
   * their timers must freeze — but a PARALLEL sibling branch, still executing, keeps ticking. Approval
   * state is app-global (ApprovalService); a frame belonging to another chat isn't in this map → no
   * freeze here (correct).
   */
  readonly parkedToolIds = computed(() => {
    const frames = this.approvals.frames();
    const out = new Set<number>();
    if (frames.size === 0) return out;
    const byId = new Map<number, ToolView>();
    for (const m of this._messages()) for (const t of m.tools) if (t.id != null) byId.set(t.id, t);
    for (const frame of frames) {
      let id: number | undefined = frame;
      while (id != null && !out.has(id)) {
        out.add(id);
        id = byId.get(id)?.parentId;
      }
    }
    return out;
  });

  constructor() {
    this.conn.onEvent((e) => this.reduce(e));

    // Resync on (re)connect or active-workspace change: re-open the active chat → fresh snapshot. Only
    // once the active workspace is one the daemon actually serves — else a stale persisted name would
    // target an unknown workspace before workspace.list reconciles it.
    effect(() => {
      if (this.conn.state() !== 'connected') return;
      const ws = this.ws.active();
      if (!ws || !this.ws.workspaces().some((w) => w.name === ws)) return;
      // Read the active chat id UNTRACKED: this effect must re-run only on (re)connect / ws change,
      // NOT when a new chat's id lands (that would fire a redundant chat.open every time it changes).
      const id = untracked(() => this._activeChatId());
      if (id) this.conn.send({ cmd: 'chat.open', ws, id });
    });
  }

  // ── commands (ws = the app-wide active workspace) ────────────────────────────

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

  /** Open a chat: clear local state, request its snapshot. */
  openChat(id: string): void {
    const ws = this.ws.active();
    if (!ws) return;
    this._activeChatId.set(id);
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

  // ── event reduction ──────────────────────────────────────────────────────────

  private reduce(e: ServerEvent): void {
    switch (e.type) {
      case 'chat.snapshot': {
        this._activeChatId.set(e.id);
        const msgs = buildSnapshotMessages(e.messages, e.tools ?? []);
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
          // The unread dot updates from the daemon's chat.activity push (handled by ChatListService).
        }
        break;

      // approval.request / approval.resolved are app-global (no chat scope) — owned by ApprovalService.
    }
  }

  private resetLocal(): void {
    this._messages.set([]);
    this._running.set(false);
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
}

/** Mint a chat id client-side: 6 random bytes as lowercase hex (matches the daemon's format, which
 * validates it before use). Client-minted ids make a new-chat submit self-identifying — no
 * server round-trip to learn the id. */
function newChatId(): string {
  const b = new Uint8Array(6);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
}
