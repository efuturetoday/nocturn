import { Injectable, signal } from '@angular/core';
import type { Source } from '../protocol/nocturn-protocol';
import { ConversationService } from './conversation.service';

/**
 * ChatService is the USER-chat conversation: the ConversationService bound to kind "user", plus the
 * message-first composer flow (a fresh chat is minted client-side and its first message auto-sent once
 * the page opens). Agent runs are the sibling AgentRunService — server-created, never minted here.
 */
@Injectable({ providedIn: 'root' })
export class ChatService extends ConversationService {
  protected readonly kind: Source = 'user';

  // First message to auto-send once the composer opens a fresh chat (consumed by the chat page).
  private readonly _pendingFirst = signal<string | null>(null);

  /** Begin a fresh chat: MINT its id client-side and make it active — the first submit creates it on
      the daemon (an unknown id starts that chat). Returns the id so the caller can navigate to it. */
  newChat(): string {
    const id = newChatId();
    this.setActive(id);
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
}

/** Mint a chat id client-side: 6 random bytes as lowercase hex (matches the daemon's format, which
 * validates it before use). Client-minted ids make a new-chat submit self-identifying — no
 * server round-trip to learn the id. */
function newChatId(): string {
  const b = new Uint8Array(6);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
}
