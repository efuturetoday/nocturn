import { Injectable, inject, signal, computed, effect } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import type { SkillInfo } from '../protocol/nocturn-protocol';

/**
 * SkillService mirrors the ACTIVE workspace's skills. State sync like the rest of the control plane:
 * it sends `skill.list` and replaces its state from the pushed `skill.list`, which the daemon
 * broadcasts after every change, and re-lists on (re)connect and on workspace switch.
 *
 * Neither the toggle nor the removal is optimistic — same reason as reminder.cancel: the daemon
 * broadcasts to every device, so a local edit would race that broadcast and two devices toggling at
 * once would converge on their own guesses instead of on the disk.
 *
 * Bodies are cached per name once read. A SKILL.md changes only when someone edits it on the host,
 * and re-reading on every open would put a round-trip in front of a screen whose whole job is to be
 * read — the list broadcast clears the cache, so an install or a removal cannot leave a stale one.
 */
@Injectable({ providedIn: 'root' })
export class SkillService {
  private readonly conn = inject(ConnectionService);
  private readonly workspaces = inject(WorkspaceService);

  private readonly _skills = signal<SkillInfo[]>([]);
  /** The active workspace's skills, enabled and disabled alike (the daemon's order). */
  readonly skills = this._skills.asReadonly();

  /** How many are actually in front of the model — the number that costs context. */
  readonly enabledCount = computed(() => this._skills().filter((s) => s.enabled).length);

  private readonly _bodies = signal<Record<string, string>>({});
  /** The SKILL.md files read so far, by skill name. */
  readonly bodies = this._bodies.asReadonly();

  constructor() {
    this.conn.onEvent((e) => {
      // Every device receives the whole daemon's traffic and routes it itself.
      if (e.type === 'skill.list') {
        if (e.ws !== this.workspaces.active()) return;
        this._skills.set(e.items);
        this._bodies.set({});
      } else if (e.type === 'skill.body') {
        if (e.ws !== this.workspaces.active()) return;
        this._bodies.update((b) => ({ ...b, [e.name]: e.body }));
      }
    });

    // Re-list on (re)connect and on workspace switch; clear first so a switch never shows the
    // previous workspace's skills while the new list is in flight.
    effect(() => {
      const ws = this.workspaces.active();
      const connected = this.conn.state() === 'connected';
      this._skills.set([]);
      this._bodies.set({});
      if (connected && ws) this.list(ws);
    });
  }

  /** Request the skills for ws (default: the active workspace). */
  list(ws?: string): void {
    const target = ws ?? this.workspaces.active();
    if (!target) return;
    this.conn.send({ cmd: 'skill.list', ws: target });
  }

  /** Fetch one skill's SKILL.md unless it is already held. */
  read(name: string): void {
    const ws = this.workspaces.active();
    if (!ws || this._bodies()[name] !== undefined) return;
    this.conn.send({ cmd: 'skill.read', ws, name });
  }

  /** Switch a skill on or off. Off is not a deletion — the folder is only moved aside. */
  enable(name: string, on: boolean): void {
    const ws = this.workspaces.active();
    if (!ws) return;
    this.conn.send({ cmd: 'skill.enable', ws, name, on });
  }

  /** Delete a skill's directory. Really deletes; the catalog is how one comes back. */
  remove(name: string): void {
    const ws = this.workspaces.active();
    if (!ws) return;
    this.conn.send({ cmd: 'skill.remove', ws, name });
  }
}
