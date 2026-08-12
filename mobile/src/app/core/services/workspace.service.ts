import { Injectable, inject, signal, computed, effect } from '@angular/core';
import { Preferences } from '@capacitor/preferences';
import { ConnectionService } from './connection.service';
import type { WorkspaceInfo } from '../protocol/nocturn-protocol';

const KEY_ACTIVE = 'nocturn.activeWorkspace';

/**
 * WorkspaceService mirrors the daemon's workspaces and owns the app-wide ACTIVE selection (the tabs
 * are scoped to it). Control plane is STATE SYNC: it sends `workspace.list` and updates its signals
 * from the pushed `workspace.list` event, re-listing on every (re)connect.
 *
 * The three mutations are deliberately NOT optimistic — the same reasoning as reminder.cancel: the
 * daemon broadcasts the new set to every device, so editing the list locally would only race that
 * broadcast, and two devices creating at once would converge on their own guesses instead of on the
 * daemon's. A rejection (a name the daemon refuses, a device without `manage`) comes back as a bare
 * `error` event, which ToastService already shows; there is nothing to roll back because nothing
 * moved.
 */
@Injectable({ providedIn: 'root' })
export class WorkspaceService {
  private readonly conn = inject(ConnectionService);

  private readonly _workspaces = signal<WorkspaceInfo[]>([]);
  readonly workspaces = this._workspaces.asReadonly();

  private readonly _active = signal<string | null>(null);
  /** The workspace the tabs are scoped to — its NAME, which is what every command addresses. */
  readonly active = this._active.asReadonly();

  /** The active workspace's entry, or null while the list is still in flight. */
  readonly activeInfo = computed(() => {
    const name = this._active();
    return this._workspaces().find((w) => w.name === name) ?? null;
  });

  /** What to call the active workspace on screen. Falls back to the name so the header is never
      blank in the moment between connecting and the first list. */
  readonly activeTitle = computed(() => this.activeInfo()?.title ?? this._active() ?? '');

  constructor() {
    void this.loadActive();

    this.conn.onEvent((e) => {
      if (e.type === 'workspace.list') {
        this._workspaces.set(e.items);
        // Reconcile the active selection against what the daemon actually serves: pick a workspace
        // when none is active yet, OR when the persisted one no longer exists (a stale name, or the
        // one you were in was just deleted) — otherwise every command targets an unknown workspace.
        // The default is preferred over the first entry: the list is sorted by name, so falling
        // back to items[0] would land wherever the alphabet points, which is not a place anyone
        // chose. The default is the one workspace that always exists.
        const active = this._active();
        const known = active !== null && e.items.some((w) => w.name === active);
        if (!known && e.items.length) {
          const fallback = e.items.find((w) => w.default) ?? e.items[0];
          void this.setActive(fallback.name);
        }
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

  /** Add a workspace. The daemon answers with a broadcast, which refreshes the list. */
  create(name: string, title?: string): void {
    this.conn.send({ cmd: 'workspace.create', name, title });
  }

  /** Change a workspace's display title. The name — the folder, the address — is untouched. */
  rename(name: string, title: string): void {
    this.conn.send({ cmd: 'workspace.rename', name, title });
  }

  /** Remove a workspace. Named for what the daemon does — it closes the workspace and MOVES its
      directory to the trash — rather than for the command it sends. */
  remove(name: string): void {
    this.conn.send({ cmd: 'workspace.delete', name });
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
