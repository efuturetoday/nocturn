import { Injectable, inject, signal, effect } from '@angular/core';
import { Preferences } from '@capacitor/preferences';
import { ConnectionService } from './connection.service';
import type { WorkspaceInfo } from '../protocol/nocturn-protocol';

const KEY_ACTIVE = 'nocturn.activeWorkspace';

/**
 * WorkspaceService mirrors the daemon's workspaces and owns the app-wide ACTIVE selection (the tabs
 * are scoped to it). Control plane is STATE SYNC: it sends `workspace.list` and updates its signals
 * from the pushed `workspace.list` event, re-listing on every (re)connect. The daemon currently
 * serves one workspace ("main"); the picker scales when it serves more.
 */
@Injectable({ providedIn: 'root' })
export class WorkspaceService {
  private readonly conn = inject(ConnectionService);

  private readonly _workspaces = signal<WorkspaceInfo[]>([]);
  readonly workspaces = this._workspaces.asReadonly();

  private readonly _active = signal<string | null>(null);
  /** The workspace the tabs are scoped to. */
  readonly active = this._active.asReadonly();

  constructor() {
    void this.loadActive();

    this.conn.onEvent((e) => {
      if (e.type === 'workspace.list') {
        this._workspaces.set(e.items);
        // Reconcile the active selection against what the daemon actually serves: pick the first
        // workspace when none is active yet, OR when the persisted one no longer exists (e.g. a
        // stale name from an older build) — otherwise every command targets an unknown workspace.
        const active = this._active();
        const known = active !== null && e.items.some((w) => w.name === active);
        if (!known && e.items.length) void this.setActive(e.items[0].name);
      }
    });

    // Resync on every (re)connect — the list is the recovery primitive.
    effect(() => {
      if (this.conn.state() === 'connected') this.list();
    });
  }

  list(): void {
    this.conn.send({ cmd: 'workspace.list' });
  }

  /** Select the active workspace (persisted). */
  async setActive(ws: string): Promise<void> {
    this._active.set(ws);
    await Preferences.set({ key: KEY_ACTIVE, value: ws });
  }

  /** The active workspace, reading persisted storage if the signal isn't populated yet (survives an
      app reload before the constructor's async load resolves). */
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
