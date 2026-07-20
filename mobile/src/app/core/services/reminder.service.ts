import { Injectable, inject, signal, effect } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import type { ReminderMeta } from '../protocol/nocturn-protocol';

/**
 * Pending reminders for the active workspace — a read-only mirror of the daemon's list. Requested
 * on (re)connect and whenever the active workspace changes, then kept live by the pushed
 * `reminders` event. The model sets/cancels reminders via its gated tools; the app only views them.
 */
@Injectable({ providedIn: 'root' })
export class ReminderService {
  private readonly conn = inject(ConnectionService);
  private readonly ws = inject(WorkspaceService);

  private readonly _reminders = signal<ReminderMeta[]>([]);
  readonly reminders = this._reminders.asReadonly();

  constructor() {
    this.conn.onEvent((e) => {
      if (e.type === 'reminders' && e.ws === this.ws.active()) this._reminders.set(e.items);
    });

    // Resync on (re)connect OR active-workspace change: clear the stale list, then re-request.
    effect(() => {
      if (this.conn.state() !== 'connected') return;
      const ws = this.ws.active();
      if (!ws) return;
      this._reminders.set([]);
      this.conn.send({ cmd: 'listReminders', ws });
    });
  }
}
