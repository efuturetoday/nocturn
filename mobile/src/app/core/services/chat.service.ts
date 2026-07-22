import { Injectable, inject, signal, computed, effect, untracked } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import { ApprovalService } from './approval.service';
import type { ServerEvent } from '../protocol/nocturn-protocol';
import type { ToolView } from './chat-view';
import { ChatView, EMPTY, seed, applyEvent, pushUser } from './chat-model';

/**
 * ChatService owns the ONE active chat's assembled state — a thin signal shell over the pure
 * `chat-model` fold. Both entry points reduce into the same `ChatView` by the same builders: a
 * `chat.snapshot` seeds it (`seed`), each live event folds onto it (`applyEvent`), so the snapshot
 * path and the live-stream path cannot drift. A chat is message-first: submitting with no active chat
 * starts one (an unknown client-minted id creates it on the daemon). `chat.snapshot` is the wholesale
 * resync primitive; on (re)connect it re-opens the active chat to pull a fresh one. The chat LIST +
 * unread state live in ChatListService; the app-global out-of-band approval state lives in
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

  private readonly _view = signal<ChatView>(EMPTY);
  readonly messages = computed(() => this._view().messages);
  readonly running = computed(() => this._view().running);

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
    for (const m of this._view().messages) {
      for (const t of m.tools) {
        if (t.id != null) byId.set(t.id, t);
      }
    }
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
    this._view.update((v) => ({ ...pushUser(v, text), running: true }));
    this.conn.send({ cmd: 'chat.submit', ws, text, id });
  }

  cancel(): void {
    const ws = this.ws.active();
    const id = this._activeChatId();
    if (ws && id) this.conn.send({ cmd: 'chat.cancel', ws, id });
  }

  // ── event reduction ──────────────────────────────────────────────────────────

  /**
   * Route one server event onto the active chat's view. `chat.snapshot` seeds wholesale; the streaming
   * events are broadcast for EVERY live chat, so drop those for a chat not on screen, then fold the
   * rest through the pure `applyEvent`. Approval events are app-global (no chat scope) — owned by
   * ApprovalService. The unread dot updates from the daemon's chat.activity push (ChatListService).
   */
  private reduce(e: ServerEvent): void {
    if (e.type === 'chat.snapshot') {
      this._activeChatId.set(e.id);
      this._view.set(seed(e, Date.now()));
      return;
    }
    if (!('chatId' in e) || e.chatId !== this._activeChatId()) return;
    this._view.update((v) => applyEvent(v, e, Date.now()));
  }

  private resetLocal(): void {
    this._view.set(EMPTY);
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
