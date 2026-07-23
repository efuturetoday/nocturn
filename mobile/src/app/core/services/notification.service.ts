import { Injectable, inject } from '@angular/core';
import { Router } from '@angular/router';
import { ToastController } from '@ionic/angular/standalone';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import { ChatService } from './chat.service';
import type { Notification } from '../protocol/nocturn-protocol';

/**
 * NotificationService is the in-app end of a proactive message — a reminder that fired, or a `notify`
 * tool call. The daemon sends it over the live connection AND as an APNs push; this handles the
 * former, which is the path that matters while the app is open (the OS suppresses or buries a push
 * for a foregrounded app, and a fired reminder is already gone from the pending list, so without this
 * the most likely case would show nothing at all).
 *
 * It shows a toast rather than a silent list entry: the message is time-relevant and there is no
 * history to browse later. Tapping "Open" lands in the chat it came from — a reminder is rarely an
 * end in itself, and the conversation that set it holds the context to act on it.
 *
 * `openTarget` is also what a PUSH tap routes through, so both paths land in the same place.
 *
 * It builds its toast directly rather than going through ToastService: that one is for transient
 * feedback (a message and a colour), while this needs a header and an action button bound to a
 * navigation target.
 */
@Injectable({ providedIn: 'root' })
export class NotificationService {
  private readonly conn = inject(ConnectionService);
  private readonly workspaces = inject(WorkspaceService);
  private readonly chat = inject(ChatService);
  private readonly router = inject(Router);
  private readonly toasts = inject(ToastController);

  constructor() {
    this.conn.onEvent((e) => {
      if (e.type === 'notification') void this.present(e);
    });
  }

  /**
   * Open what a notification points at: its workspace, and the chat it came from when there is one.
   * Used by both the in-app toast and a push tap.
   */
  async openTarget(ws: string, chatId?: string): Promise<void> {
    // The notification may come from a workspace the app isn't scoped to; switching first means the
    // chat we open is resolved against the right one.
    if (ws && ws !== this.workspaces.active()) await this.workspaces.setActive(ws);
    if (!chatId) return;
    this.chat.openChat(chatId);
    await this.router.navigate(['/chat', chatId]);
  }

  private async present(n: Notification): Promise<void> {
    const toast = await this.toasts.create({
      header: n.title || (n.kind === 'remind' ? 'Reminder' : 'Nocturn'),
      message: n.message,
      // Long enough to read a sentence, not sticky: a reminder that scrolled past is not lost, the
      // push carries the same text to the notification centre.
      duration: 6000,
      position: 'top',
      color: n.kind === 'remind' ? 'primary' : 'medium',
      buttons: n.chatId
        ? [{ text: 'Open', handler: () => void this.openTarget(n.ws, n.chatId) }]
        : [{ text: 'Dismiss', role: 'cancel' }],
    });
    await toast.present();
  }
}
