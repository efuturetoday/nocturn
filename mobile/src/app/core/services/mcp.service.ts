import { Injectable, inject, signal, computed, effect } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import type { MCPInfo } from '../protocol/nocturn-protocol';

/**
 * McpService mirrors the ACTIVE workspace's MCP servers. State sync, like its siblings: it sends
 * `mcp.list` and replaces its state from the pushed one, re-listing on (re)connect and on workspace
 * switch.
 *
 * The one thing that is not like its siblings: a single mutation produces TWO lists. The daemon
 * writes the declaration, answers at once with `connecting` for the server nobody has tried yet, and
 * broadcasts again when the handshakes are through — up to thirty seconds per server. Nothing here
 * has to do anything special about that, and that is the point: replacing the state from whatever
 * arrived shows the intermediate state for free. Any attempt to be clever — suppressing the first
 * frame, or holding an optimistic row until the second — would be the thing that breaks it.
 */
@Injectable({ providedIn: 'root' })
export class McpService {
  private readonly conn = inject(ConnectionService);
  private readonly workspaces = inject(WorkspaceService);

  private readonly _servers = signal<MCPInfo[]>([]);
  /** The active workspace's DECLARED servers, connected or not (the daemon's order). */
  readonly servers = this._servers.asReadonly();

  /** True while any server is mid-handshake — the list is not final yet. */
  readonly settling = computed(() => this._servers().some((s) => s.state === 'connecting'));

  /** The tools all connected servers contribute together. */
  readonly toolCount = computed(() => this._servers().reduce((n, s) => n + s.tools, 0));

  constructor() {
    this.conn.onEvent((e) => {
      if (e.type === 'mcp.list' && e.ws === this.workspaces.active()) this._servers.set(e.items);
    });

    effect(() => {
      const ws = this.workspaces.active();
      const connected = this.conn.state() === 'connected';
      this._servers.set([]);
      if (connected && ws) this.list(ws);
    });
  }

  /** Request the servers for ws (default: the active workspace). */
  list(ws?: string): void {
    const target = ws ?? this.workspaces.active();
    if (!target) return;
    this.conn.send({ cmd: 'mcp.list', ws: target });
  }

  /** Declare a server. No credential travels with it — see MCPAddCmd. */
  add(name: string, url: string, auth?: string): void {
    const ws = this.workspaces.active();
    if (!ws) return;
    this.conn.send({ cmd: 'mcp.add', ws, name, url, auth });
  }

  /** Drop a server, its secret shard, and the remembered network grant for its host. */
  remove(name: string): void {
    const ws = this.workspaces.active();
    if (!ws) return;
    this.conn.send({ cmd: 'mcp.remove', ws, name });
  }

  /**
   * Re-read the workspace from disk: the way out of `failed`, and the step after connecting an
   * account.
   *
   * It is a WORKSPACE command, not an MCP one, because re-running discovery re-runs all of it —
   * agents, skills, plugins and servers. Both lists follow when it lands.
   */
  reload(): void {
    const ws = this.workspaces.active();
    if (!ws) return;
    this.conn.send({ cmd: 'workspace.reload', ws });
  }
}
