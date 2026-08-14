import { Injectable, inject, signal, computed, effect } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import type { GrantInfo } from '../protocol/nocturn-protocol';

/**
 * GrantService mirrors the ACTIVE workspace's standing approvals — the permissions the gate no
 * longer asks about.
 *
 * Every other control-plane service answers "what is this workspace made of". This one answers what
 * it may DO without asking again, which is where authority quietly accumulates: the gate asks on a
 * new action and remembers the answer, and a remembered answer outlives the reason it was given for.
 * Nothing showed them until now.
 *
 * Revoking is not optimistic, like every other change here: the daemon broadcasts the new list to
 * every device, and a local edit would race that broadcast.
 */
@Injectable({ providedIn: 'root' })
export class GrantService {
  private readonly conn = inject(ConnectionService);
  private readonly workspaces = inject(WorkspaceService);

  private readonly _grants = signal<GrantInfo[]>([]);
  /** The active workspace's standing approvals, sorted by the daemon (kind, then target). */
  readonly grants = this._grants.asReadonly();

  /** The ones written down, which are the ones that outlive a restart — and accumulate. */
  readonly durableCount = computed(() => this._grants().filter((g) => g.durable).length);

  constructor() {
    this.conn.onEvent((e) => {
      if (e.type !== 'grant.list') return;
      if (e.ws !== this.workspaces.active()) return;
      this._grants.set(e.items);
    });

    // Clear first: a permission from the previous workspace shown against this one is a wrong
    // answer, not a stale one.
    effect(() => {
      const ws = this.workspaces.active();
      const connected = this.conn.state() === 'connected';
      this._grants.set([]);
      if (connected && ws) this.list(ws);
    });
  }

  /** Request the standing approvals for ws (default: the active workspace). */
  list(ws?: string): void {
    const target = ws ?? this.workspaces.active();
    if (!target) return;
    this.conn.send({ cmd: 'grant.list', ws: target });
  }

  /** Take one back. The next action of that shape asks again. */
  forget(kind: string, target: string): void {
    const ws = this.workspaces.active();
    if (!ws) return;
    this.conn.send({ cmd: 'grant.forget', ws, kind, target });
  }
}
