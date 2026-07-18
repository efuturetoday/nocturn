import { Injectable, inject, signal, effect } from '@angular/core';
import { Preferences } from '@capacitor/preferences';
import { ConnectionService } from './connection.service';
import type { WorkspaceSummary, WorkspaceState } from '../protocol/nocturn-protocol';

const KEY_ACTIVE = 'nocturn.activeWorkspace';

/**
 * WorkspaceService mirrors the daemon's workspace picker + detail as signals, and owns the
 * app-wide ACTIVE workspace selection (the tabs are scoped to it). Control plane is STATE
 * SYNC: it sends `listWorkspaces`/`getWorkspace`/`setPersona` and updates its signals from the
 * pushed `workspaces`/`workspace` events. It re-lists (and re-fetches the active detail) on
 * every (re)connect.
 */
@Injectable({ providedIn: 'root' })
export class WorkspaceService {
  private readonly conn = inject(ConnectionService);

  private readonly _workspaces = signal<WorkspaceSummary[]>([]);
  readonly workspaces = this._workspaces.asReadonly();

  private readonly _selected = signal<WorkspaceState | null>(null);
  readonly selected = this._selected.asReadonly();

  private readonly _active = signal<string | null>(null);
  /** The workspace the tabs are scoped to. */
  readonly active = this._active.asReadonly();

  private readonly _error = signal<string | null>(null);
  readonly error = this._error.asReadonly();

  constructor() {
    void this.loadActive();

    this.conn.onEvent((e) => {
      switch (e.type) {
        case 'workspaces':
          this._workspaces.set(e.items);
          // Auto-select the first workspace if none is active yet, so the tabs shell always
          // has a scope right after connect (no separate picker screen needed).
          if (!this._active() && e.items.length) void this.setActive(e.items[0].name);
          break;
        case 'workspace':
          this._selected.set(e);
          break;
        case 'error':
          this._error.set(e.text);
          break;
      }
    });

    // Resync on every (re)connect — the list + active detail are the recovery primitives.
    effect(() => {
      if (this.conn.state() !== 'connected') return;
      this.list();
      const a = this._active();
      if (a) this.get(a);
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

  /** Select the active workspace (persisted) and fetch its detail. */
  async setActive(ws: string): Promise<void> {
    this._active.set(ws);
    this.get(ws);
    await Preferences.set({ key: KEY_ACTIVE, value: ws });
  }

  /** The active workspace, reading persisted storage if the signal isn't populated yet
      (survives app reload before the constructor's async load resolves). */
  async activeValue(): Promise<string | null> {
    if (this._active()) return this._active();
    const { value } = await Preferences.get({ key: KEY_ACTIVE });
    if (value) this._active.set(value);
    return value ?? null;
  }

  private async loadActive(): Promise<void> {
    const { value } = await Preferences.get({ key: KEY_ACTIVE });
    if (value) this._active.set(value);
  }
}
