import { Injectable, inject, signal, effect } from '@angular/core';
import { ConnectionService } from './connection.service';
import type { WorkspaceSummary, WorkspaceState } from '../protocol/nocturn-protocol';

/**
 * WorkspaceService mirrors the daemon's workspace picker + detail as signals. Control plane
 * is STATE SYNC: it sends `listWorkspaces`/`getWorkspace`/`setPersona` and updates its signals
 * from the pushed `workspaces`/`workspace` events. It re-lists automatically on (re)connect.
 */
@Injectable({ providedIn: 'root' })
export class WorkspaceService {
  private readonly conn = inject(ConnectionService);

  private readonly _workspaces = signal<WorkspaceSummary[]>([]);
  readonly workspaces = this._workspaces.asReadonly();

  private readonly _selected = signal<WorkspaceState | null>(null);
  readonly selected = this._selected.asReadonly();

  private readonly _error = signal<string | null>(null);
  readonly error = this._error.asReadonly();

  constructor() {
    this.conn.onEvent((e) => {
      switch (e.type) {
        case 'workspaces':
          this._workspaces.set(e.items);
          break;
        case 'workspace':
          this._selected.set(e);
          break;
        case 'error':
          this._error.set(e.text);
          break;
      }
    });

    // Resync on every (re)connect — the snapshot/list is the recovery primitive.
    effect(() => {
      if (this.conn.state() === 'connected') this.list();
    });
  }

  list(): void {
    this.conn.send({ cmd: 'listWorkspaces' });
  }

  get(ws: string): void {
    this.conn.send({ cmd: 'getWorkspace', ws });
  }

  setPersona(ws: string, text: string): void {
    this.conn.send({ cmd: 'setPersona', ws, text });
  }
}
