import { Injectable, inject, signal, computed, effect } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import type { ReminderInfo } from '../protocol/nocturn-protocol';

/**
 * ReminderService mirrors the ACTIVE workspace's pending reminders. Like the rest of the control
 * plane it is STATE SYNC, not RPC: it sends `reminder.list` and replaces its state from the pushed
 * `reminder.list` event, re-listing whenever the daemon says the set changed (`reminder.changed`),
 * whenever the active workspace switches, and on every (re)connect.
 *
 * There is no create: reminders are set by the model through the gated `remind` tool. A fired
 * reminder leaves this set and arrives as a push instead — the list is pending-only, never a history.
 *
 * Cancel is deliberately NOT optimistic. The daemon broadcasts `reminder.changed` to every device, so
 * removing the row locally would only race that broadcast; two devices cancelling at once converge on
 * the daemon's set instead of on their own guesses.
 */
@Injectable({ providedIn: 'root' })
export class ReminderService {
  private readonly conn = inject(ConnectionService);
  private readonly workspaces = inject(WorkspaceService);

  private readonly _reminders = signal<ReminderInfo[]>([]);
  /** The active workspace's pending reminders, soonest first (the daemon's order). */
  readonly reminders = this._reminders.asReadonly();

  readonly count = computed(() => this._reminders().length);

  constructor() {
    this.conn.onEvent((e) => {
      // Ignore events for a workspace the app isn't showing: every device receives the whole
      // daemon's traffic and routes it itself.
      if (e.type === 'reminder.list') {
        if (e.ws === this.workspaces.active()) this._reminders.set(e.reminders);
      } else if (e.type === 'reminder.changed') {
        if (e.ws === this.workspaces.active()) this.list();
      }
    });

    // Re-list on (re)connect and whenever the active workspace changes; clear first so a switch never
    // shows the previous workspace's reminders while the new list is in flight.
    effect(() => {
      const ws = this.workspaces.active();
      const connected = this.conn.state() === 'connected';
      this._reminders.set([]);
      if (connected && ws) this.list(ws);
    });
  }

  /** Request the pending reminders for ws (default: the active workspace). */
  list(ws?: string): void {
    const target = ws ?? this.workspaces.active();
    if (!target) return;
    this.conn.send({ cmd: 'reminder.list', ws: target });
  }

  /** Drop a pending reminder. The daemon answers with a broadcast, which refreshes the list. */
  cancel(id: string): void {
    const ws = this.workspaces.active();
    if (!ws) return;
    this.conn.send({ cmd: 'reminder.cancel', ws, id });
  }
}
