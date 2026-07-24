import { inject, signal, computed, effect, untracked } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import { ApprovalService } from './approval.service';
import type { ServerEvent, Source } from '../protocol/nocturn-protocol';
import type { ToolView } from './chat-view';
import { ChatView, EMPTY, seed, applyEvent, pushUser } from './chat-model';

/**
 * ConversationService is the ONE active conversation's assembled state — a thin signal shell over the
 * pure `chat-model` fold, shared by the user-chat and agent-run services. A subclass fixes `kind`
 * ("user" | "agent"), which the daemon needs on every store-addressed command; the client never
 * tracks kind as mutable state — it IS the concrete service you injected. Both entry points reduce
 * into the same `ChatView` by the same builders: a `chat.snapshot` seeds it (`seed`), each live event
 * folds onto it (`applyEvent`), so the snapshot path and the live stream cannot drift.
 *
 * The two subclasses are independent root singletons, each with its own active conversation: a live
 * event or snapshot is consumed only if it matches THIS service's active id — so a user chat and an
 * agent run stream side by side without either seeding the other. Approval state is app-global
 * (ApprovalService) — this service only reads `frames()` to freeze a parked branch's timers.
 */
export abstract class ConversationService {
  /** The store this conversation lives in — fixed per subclass, sent on every chat.* command. */
  protected abstract readonly kind: Source;

  protected readonly conn = inject(ConnectionService);
  protected readonly ws = inject(WorkspaceService);
  private readonly approvals = inject(ApprovalService);

  private readonly _activeChatId = signal<string | null>(null);
  readonly activeChatId = this._activeChatId.asReadonly();

  private readonly _view = signal<ChatView>(EMPTY);
  readonly messages = computed(() => this._view().messages);
  readonly running = computed(() => this._view().running);

  /** The ids of every tool call frozen by an open approval: each approval's named tool frame PLUS its
      ancestors, walked up the parentId chain in THIS conversation's tool map. A parallel sibling
      branch, still executing, keeps ticking; a frame from another conversation isn't in this map. */
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

    // Resync on (re)connect or active-workspace change: re-open the active conversation → fresh
    // snapshot. Only once the active workspace is one the daemon serves (else a stale name targets an
    // unknown workspace before workspace.list reconciles it).
    effect(() => {
      if (this.conn.state() !== 'connected') return;
      const ws = this.ws.active();
      if (!ws || !this.ws.workspaces().some((w) => w.name === ws)) return;
      // UNTRACKED: re-run only on (re)connect / ws change, not when the active id lands.
      const id = untracked(() => this._activeChatId());
      if (id) this.conn.send({ cmd: 'chat.open', ws, kind: this.kind, id });
    });
  }

  /** Open a conversation: make it active, clear local state, request its snapshot. */
  openChat(id: string): void {
    const ws = this.ws.active();
    if (!ws) return;
    this._activeChatId.set(id);
    this._view.set(EMPTY);
    this.conn.send({ cmd: 'chat.open', ws, kind: this.kind, id });
  }

  /** Send a message: optimistically echo the user bubble, then stream the reply (the assistant bubble
      emerges from the stream's first frame-0 event, so a local turn and a backend-initiated one render
      identically). Addressed by the active id. */
  submit(input: string): void {
    const text = input.trim();
    const ws = this.ws.active();
    const id = this._activeChatId();
    if (!text || !ws || !id) return;
    this._view.update((v) => ({ ...pushUser(v, text), running: true }));
    this.conn.send({ cmd: 'chat.submit', ws, kind: this.kind, text, id });
  }

  cancel(): void {
    const ws = this.ws.active();
    const id = this._activeChatId();
    if (ws && id) this.conn.send({ cmd: 'chat.cancel', ws, kind: this.kind, id });
  }

  /** Make a client-minted id active with a clean view — for a fresh chat whose first submit creates it
      on the daemon. Protected: only a subclass that mints ids (user chats) uses it. */
  protected setActive(id: string): void {
    this._activeChatId.set(id);
    this._view.set(EMPTY);
  }

  /**
   * Route one server event onto this conversation's view. A `chat.snapshot` seeds wholesale, but only
   * for the id THIS service opened (its activeChatId) — so the sibling service's snapshot is ignored.
   * The streaming events are broadcast for every live chat, so drop those whose chatId isn't active,
   * then fold the rest through the pure `applyEvent`.
   */
  private reduce(e: ServerEvent): void {
    if (e.type === 'chat.snapshot') {
      if (e.id !== this._activeChatId()) return;
      this._view.set(seed(e, Date.now()));
      return;
    }
    if (!('chatId' in e) || e.chatId !== this._activeChatId()) return;
    this._view.update((v) => applyEvent(v, e, Date.now()));
  }
}
