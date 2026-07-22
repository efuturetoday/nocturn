import { Injectable, inject, signal, computed } from '@angular/core';
import { ConnectionService } from './connection.service';
import type { ServerEvent } from '../protocol/nocturn-protocol';
import type { PendingApproval } from './chat-view';

/**
 * ApprovalService owns the app-wide out-of-band approval state. An `approval.request` carries NO chat
 * id at the source (the broker sits below the chat layer) — it is inherently global, so it lives here
 * and NOT on any per-chat service that chat navigation would reset. The daemon re-presents every open
 * approval on each (re)connect and on `presence.set active=true`, so the reducer dedupes by id.
 *
 * The prompt is surfaced app-globally by ApprovalOverlayComponent (mounted in the app root), whose
 * visibility is a pure `@if (pending().length)` binding — so a request shows on any route (including
 * one raised by a background agent run while you are elsewhere) and never lingers empty.
 */
@Injectable({ providedIn: 'root' })
export class ApprovalService {
  private readonly conn = inject(ConnectionService);

  private readonly _pending = signal<PendingApproval[]>([]);
  /** Every open approval, in arrival order (the daemon allows several concurrently). */
  readonly pending = this._pending.asReadonly();

  /** True while any approval awaits an answer — drives the auto-presented sheet. */
  readonly has = computed(() => this._pending().length > 0);

  /** The tool-call frames of all open approvals — lets a tool frame show "needs approval" and freeze
   * its timer (the parked branch) without knowing which specific approval named it. */
  readonly frames = computed(() => {
    const out = new Set<number>();
    for (const p of this._pending()) if (p.frame != null) out.add(p.frame);
    return out;
  });

  constructor() {
    this.conn.onEvent((e) => this.reduce(e));
  }

  /** Answer an approval by id: the chosen option index, or -1 to deny. Optimistically drop it locally;
   * the daemon's approval.resolved broadcast confirms (and covers a resolution from another device). */
  resolve(id: string, choice: number): void {
    this.conn.send({ cmd: 'approval.resolve', id, choice });
    this._pending.update((ps) => ps.filter((p) => p.id !== id));
  }

  private reduce(e: ServerEvent): void {
    switch (e.type) {
      case 'approval.request':
        // Dedupe by id: a fresh connection / foreground re-presents all open approvals.
        this._pending.update((ps) =>
          ps.some((p) => p.id === e.id)
            ? ps
            : [...ps, { id: e.id, frame: e.frame, chatId: e.chatId, intent: e.intent, options: e.options }],
        );
        break;

      case 'approval.resolved':
        this._pending.update((ps) => ps.filter((p) => p.id !== e.id));
        break;
    }
  }
}
