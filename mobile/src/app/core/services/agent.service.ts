import { Injectable, inject, signal, effect } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import type { AgentInfo, ServerEvent } from '../protocol/nocturn-protocol';

/**
 * AgentService owns the declared-agent ROSTER per workspace (agent.list) and triggers runs
 * (agent.fire). It is the declarations layer — the runs an agent produces are chats, listed via
 * ChatListService (source "agent") and opened through AgentRunService. Mirrors ChatListService: it
 * reduces its own event and re-lists on (re)connect / workspace change.
 */
@Injectable({ providedIn: 'root' })
export class AgentService {
  private readonly conn = inject(ConnectionService);
  private readonly ws = inject(WorkspaceService);

  private readonly _agents = signal<AgentInfo[]>([]);
  readonly agents = this._agents.asReadonly();

  constructor() {
    this.conn.onEvent((e) => this.reduce(e));

    // Resync on (re)connect or active-workspace change, once the active workspace is one the daemon
    // serves (else a stale name targets an unknown workspace before workspace.list reconciles it).
    effect(() => {
      if (this.conn.state() !== 'connected') return;
      const ws = this.ws.active();
      if (!ws || !this.ws.workspaces().some((w) => w.name === ws)) return;
      this.conn.send({ cmd: 'agent.list', ws });
    });
  }

  /** Trigger an agent run now (optional task overrides the scheduled prompt). Fire-and-forget: the run
      appears in the agent-run list via chat.activity and streams like any chat. */
  fire(name: string, task?: string): void {
    const ws = this.ws.active();
    if (ws) this.conn.send({ cmd: 'agent.fire', ws, name, task });
  }

  private reduce(e: ServerEvent): void {
    if (e.type === 'agent.list' && e.ws === this.ws.active()) this._agents.set(e.agents);
  }
}
