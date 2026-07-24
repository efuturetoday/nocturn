import { Injectable, inject, signal, computed, effect } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import type { ChatMeta, ServerEvent } from '../protocol/nocturn-protocol';

/**
 * ChatListService owns the chat LIST per workspace + the shared read-state that drives the unread
 * dots. It reduces `chat.list` (wholesale) + `chat.activity` (per-chat push) into `chats`, and tracks
 * which chat is on screen so a streaming turn keeps itself read. It is deliberately independent of
 * ChatService (the active-chat reducer): both subscribe to the connection's event stream on their own
 * and each ignores the other's events — no cross-service dependency.
 */
@Injectable({ providedIn: 'root' })
export class ChatListService {
  private readonly conn = inject(ConnectionService);
  private readonly ws = inject(WorkspaceService);

  private readonly _chats = signal<ChatMeta[]>([]);
  readonly chats = this._chats.asReadonly();

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

  /** Unread counts split by chat kind (agent runs badge the Agents tab, not Chat). */
  private readonly agentChatIds = computed(() => new Set(this._chats().filter((c) => c.source === 'agent').map((c) => c.id)));
  readonly unreadUserCount = computed(() => [...this.unreadIds()].filter((id) => !this.agentChatIds().has(id)).length);
  readonly unreadAgentCount = computed(() => [...this.unreadIds()].filter((id) => this.agentChatIds().has(id)).length);

  constructor() {
    this.conn.onEvent((e) => this.reduce(e));

    // Resync on (re)connect or active-workspace change: re-list the chats. Only once the active
    // workspace is one the daemon actually serves — else a stale persisted name would target an
    // unknown workspace before workspace.list reconciles it.
    effect(() => {
      if (this.conn.state() !== 'connected') return;
      const ws = this.ws.active();
      if (!ws || !this.ws.workspaces().some((w) => w.name === ws)) return;
      this.listFor(ws);
    });
  }

  listChats(): void {
    const ws = this.ws.active();
    if (ws) this.listFor(ws);
  }

  /** List BOTH stores: user chats and agent runs land in one list (each view filters by source). The
      list is otherwise kept live by the per-chat chat.activity push, which covers both stores. */
  private listFor(ws: string): void {
    this.conn.send({ cmd: 'chat.list', ws, kind: 'user' });
    this.conn.send({ cmd: 'chat.list', ws, kind: 'agent' });
  }

  startViewing(id: string): void {
    this._viewing.set(id);
    this.clearBadge(id);
    this.markRead(id);
  }

  stopViewing(id: string): void {
    if (this._viewing() === id) this._viewing.set(null);
  }

  private reduce(e: ServerEvent): void {
    switch (e.type) {
      case 'chat.list':
        // Two lists arrive (one per kind); each replaces only its own kind's chats, so the merged
        // list holds both without one wholesale-set clobbering the other.
        if (e.ws === this.ws.active()) {
          const isAgent = e.kind === 'agent';
          this._chats.update((cs) => [...cs.filter((c) => (c.source === 'agent') !== isAgent), ...e.chats]);
        }
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
    }
  }

  /** Advance a chat's shared read cursor on the daemon (clears its dot on every device). The store is
      the chat's own kind, read from the metadata this list already holds — not tracked state. */
  private markRead(id: string): void {
    const ws = this.ws.active();
    if (!ws) return;
    const kind = this._chats().find((c) => c.id === id)?.source ?? 'user';
    this.conn.send({ cmd: 'chat.markRead', ws, kind, id });
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
}

/** A chat is unread when it has activity past its shared read cursor (or was never read). */
function isUnread(c: ChatMeta): boolean {
  return !c.read || new Date(c.updated) > new Date(c.read);
}
