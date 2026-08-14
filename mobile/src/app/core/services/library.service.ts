import { Injectable, inject, signal, computed, effect } from '@angular/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import type { LibraryCatalog } from '../protocol/nocturn-protocol';

/**
 * LibraryService holds the installable catalog.
 *
 * Unlike its siblings it is NOT workspace-scoped: a catalog is the same wherever the daemon serves,
 * and only `install` picks a target — the active workspace. It also does not list on connect. A
 * daemon without a catalog answers `library.list` with an error, and listing eagerly would greet
 * every such household with a failure toast at startup for a page they never opened. So the page
 * asks, once, when it is opened.
 *
 * "No catalog" is ABSENT, not empty, and the two must not look alike: an empty catalog is a
 * configured library with nothing in it, while an absent one is a daemon that was never pointed at
 * a URL. The wire says so only through an `error`, which carries no correlation id — so a request
 * is treated as answered by whichever comes first, the catalog or an error, and the error's own
 * words are what the page shows. The narrow cost: an unrelated error landing inside that window is
 * attributed here. It is still the daemon's true sentence, only on the wrong screen, and the next
 * refresh corrects it.
 */
@Injectable({ providedIn: 'root' })
export class LibraryService {
  private readonly conn = inject(ConnectionService);
  private readonly workspaces = inject(WorkspaceService);

  private readonly _catalog = signal<LibraryCatalog | null>(null);
  /** The catalog, or null until one has been fetched. */
  readonly catalog = this._catalog.asReadonly();

  private readonly _loading = signal(false);
  readonly loading = this._loading.asReadonly();

  private readonly _unavailable = signal<string | null>(null);
  /** The daemon's own sentence about why there is no catalog, or null if there is one. */
  readonly unavailable = this._unavailable.asReadonly();

  private readonly _installing = signal<string | null>(null);
  /** The catalog id currently being installed (drives a spinner), or null. */
  readonly installing = this._installing.asReadonly();

  readonly version = computed(() => this._catalog()?.version ?? '');

  /** Set once the page has asked, so a reconnect re-asks. A command sent while the socket is down is
      dropped on the floor, and the page only asks on the tick it opens. */
  private readonly wanted = signal(false);

  constructor() {
    this.conn.onEvent((e) => {
      switch (e.type) {
        case 'library.catalog':
          this._catalog.set(e);
          this._unavailable.set(null);
          this._loading.set(false);
          break;
        // An install lands as the target domain's own list — which is what the pages already render,
        // so there is nothing to reconcile here beyond dropping the spinner.
        case 'skill.list':
        case 'mcp.list':
          this._installing.set(null);
          break;
        case 'error':
          if (this._loading()) {
            this._loading.set(false);
            this._unavailable.set(e.text);
          }
          this._installing.set(null);
          break;
      }
    });

    effect(() => {
      const connected = this.conn.state() === 'connected';
      if (connected && this.wanted() && !this._catalog()) this.send('library.list');
    });
  }

  /** Fetch the catalog unless one is already held. Call when the page opens. The daemon serves this
      from its cache when it can, so re-opening the page costs nothing. */
  list(): void {
    this.wanted.set(true);
    if (this._catalog() || this._loading()) return;
    this.send('library.list');
  }

  /** Re-fetch past the daemon's cache. Pull-to-refresh. */
  refresh(): void {
    this.wanted.set(true);
    this.send('library.refresh');
  }

  private send(cmd: 'library.list' | 'library.refresh'): void {
    this._loading.set(true);
    this._unavailable.set(null);
    this.conn.send({ cmd });
  }

  /**
   * Install one entry into the active workspace. Sends the ID and nothing else: the daemon looks the
   * content up in the catalog it fetched itself, which is why there is no wire form that could carry
   * an edited body — see LibraryInstallCmd.
   */
  install(kind: 'skill' | 'mcp' | 'plugin', id: string): void {
    const ws = this.workspaces.active();
    if (!ws) return;
    this._installing.set(id);
    this.conn.send({ cmd: 'library.install', ws, kind, id });
  }
}
