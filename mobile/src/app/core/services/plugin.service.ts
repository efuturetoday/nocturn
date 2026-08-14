import { Injectable, inject, signal, effect } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import type { PluginInfo } from '../protocol/nocturn-protocol';

/**
 * PluginService mirrors the ACTIVE workspace's installed plugins — the same state sync the rest of
 * the control plane uses: send `plugin.list`, replace the state from the pushed one, re-list on
 * (re)connect and on a workspace switch.
 *
 * It exists for one screen and one question: the library has to know whether an entry is already
 * installed, and for a plugin nothing else on the client could answer that. Skills are matched by
 * their frontmatter name and MCP servers by their folder; a plugin's identity is its folder too, and
 * only the daemon knows what is in it.
 *
 * There is no install or remove here. Installing is `library.install`, which carries an id and never
 * code; removing is not on the wire yet, because it has to revoke the remembered permission for the
 * hosts the plugin's credential rode to before it can be offered as a button.
 */
@Injectable({ providedIn: 'root' })
export class PluginService {
  private readonly conn = inject(ConnectionService);
  private readonly workspaces = inject(WorkspaceService);

  private readonly _plugins = signal<PluginInfo[]>([]);
  /** The active workspace's installed plugins, in the daemon's order. */
  readonly plugins = this._plugins.asReadonly();

  constructor() {
    this.conn.onEvent((e) => {
      if (e.type !== 'plugin.list') return;
      if (e.ws !== this.workspaces.active()) return;
      this._plugins.set(e.items);
    });

    // Clear first, so a switch never shows the previous workspace's plugins while the new list is
    // in flight — an "installed" marker from another workspace is a wrong answer, not a stale one.
    effect(() => {
      const ws = this.workspaces.active();
      const connected = this.conn.state() === 'connected';
      this._plugins.set([]);
      if (connected && ws) this.list(ws);
    });
  }

  /** Request the plugins for ws (default: the active workspace). */
  list(ws?: string): void {
    const target = ws ?? this.workspaces.active();
    if (!target) return;
    this.conn.send({ cmd: 'plugin.list', ws: target });
  }
}
