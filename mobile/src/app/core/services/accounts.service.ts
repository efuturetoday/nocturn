import { Injectable, inject, signal, computed, effect } from '@angular/core';
import { InAppBrowser } from '@capacitor/inappbrowser';
import { App } from '@capacitor/app';
import type { PluginListenerHandle } from '@capacitor/core';
import { ConnectionService } from './connection.service';
import { WorkspaceService } from './workspace.service';
import { ToastService } from './toast.service';
import type { Account } from '../protocol/nocturn-protocol';

/** An in-flight connect: the daemon's session id, the workspace it belongs to, and the redirect the
    in-app browser must navigate to before we lift the code. */
interface Flow {
  id: string;
  ws: string;
  redirectPrefix: string;
  handles: PluginListenerHandle[];
}

/**
 * AccountsService drives the app half of MCP OAuth. The daemon runs the whole spec flow; the app only
 * opens the consent URL in the EXTERNAL browser (real Safari / Chrome — so the user's password manager
 * works, which an embedded web view would break per RFC 8252, and — unlike SFSafariViewController — a
 * custom-scheme redirect actually reopens the app), catches the deep-link redirect the OS routes back,
 * and relays the single-use, PKCE-bound `code`+`state` over the WebSocket. The access token is minted,
 * held and refreshed in the daemon and NEVER reaches the app — this service never sees it.
 *
 * Like the rest of the control plane it is state-sync: it lists the active workspace's connectable
 * accounts (`auth.list` → `auth.accounts`) and re-lists on connect, on workspace switch, and after a
 * successful connect. Only one connect runs at a time (a browser is modal anyway).
 */
@Injectable({ providedIn: 'root' })
export class AccountsService {
  private readonly conn = inject(ConnectionService);
  private readonly workspaces = inject(WorkspaceService);
  private readonly toast = inject(ToastService);

  private readonly _accounts = signal<Account[]>([]);
  /** The active workspace's connectable MCP accounts and their status (the daemon's order). */
  readonly accounts = this._accounts.asReadonly();

  private readonly _connecting = signal<string | null>(null);
  /** The server currently being connected (drives a spinner), or null. */
  readonly connecting = this._connecting.asReadonly();
  readonly busy = computed(() => this._connecting() !== null);

  /** The workspace a begin was sent for, by server name — auth.open carries the server, not the ws. */
  private readonly beganWs = new Map<string, string>();
  private flow: Flow | null = null;

  constructor() {
    this.conn.onEvent((e) => {
      switch (e.type) {
        case 'auth.accounts':
          if (e.ws === this.workspaces.active()) this._accounts.set(e.accounts);
          break;
        case 'auth.open':
          void this.openConsent(e.id, e.server, e.url, e.redirectPrefix);
          break;
        case 'auth.done':
          this.finish(e.id, e.ok, e.error);
          break;
        case 'error':
          // A control error while a connect is in flight (e.g. a locked vault surfaced by the shared
          // failure path) never arrives as auth.done — clear the spinner so it can't spin forever.
          if (this.busy()) this._connecting.set(null);
          break;
      }
    });

    // Re-list on (re)connect and on workspace switch; clear first so a switch never shows the previous
    // workspace's accounts while the new list is in flight.
    effect(() => {
      const ws = this.workspaces.active();
      const connected = this.conn.state() === 'connected';
      this._accounts.set([]);
      if (connected && ws) this.list(ws);
    });
  }

  /** Request the connectable accounts for ws (default: the active workspace). */
  list(ws?: string): void {
    const target = ws ?? this.workspaces.active();
    if (target) this.conn.send({ cmd: 'auth.list', ws: target });
  }

  /** Start connecting a discover-mode MCP account. No-op while another connect is in flight. */
  connect(server: string, scopes?: string[]): void {
    const ws = this.workspaces.active();
    if (!ws || this.busy()) return;
    this.beganWs.set(server, ws);
    this._connecting.set(server);
    this.conn.send({ cmd: 'auth.begin', ws, server, scopes });
  }

  /** Open the consent URL in the external browser and catch the deep-link redirect back into the app. */
  private async openConsent(id: string, server: string, url: string, redirectPrefix: string): Promise<void> {
    const ws = this.beganWs.get(server) ?? this.workspaces.active();
    if (!ws) return;
    const flow: Flow = { id, ws, redirectPrefix, handles: [] };
    this.flow = flow;

    // The authorization server redirects to our custom scheme; the external browser hands it to the OS,
    // which reopens the app with the deep link. We lift code+state and relay them.
    flow.handles.push(
      await App.addListener('appUrlOpen', ({ url: opened }) => {
        if (!opened.startsWith(redirectPrefix)) return; // some other deep link
        const q = new URL(opened).searchParams;
        void this.relay(flow, q.get('code') ?? '', q.get('state') ?? '');
      }),
    );

    await InAppBrowser.openInExternalBrowser({ url });
  }

  /** Relay the code back and tear down the browser + listeners. auth.done drives the rest. */
  private async relay(flow: Flow, code: string, state: string): Promise<void> {
    await this.teardown(flow);
    this.conn.send({ cmd: 'auth.callback', ws: flow.ws, id: flow.id, code, state });
  }

  /** Finish (auth.done): clear the spinner, toast the outcome, and re-list on success. `id` is absent
      when auth.begin failed before minting a session. */
  private finish(id: string | undefined, ok: boolean, error?: string): void {
    if (this.flow && id && this.flow.id === id) void this.teardown(this.flow);
    this._connecting.set(null);
    if (ok) {
      void this.toast.show('Account connected', 'success');
      this.list();
    } else {
      void this.toast.show(error ?? 'Could not connect the account', 'danger');
    }
  }

  /** Abandon an in-flight flow (user-closed browser): drop the spinner, no callback. */
  private cancel(flow: Flow): void {
    void this.teardown(flow);
    this._connecting.set(null);
  }

  /** Remove this flow's listeners. The external browser stays where it is (the OS foregrounded the app
      over it); there is no owned browser to close. */
  private async teardown(flow: Flow): Promise<void> {
    if (this.flow === flow) this.flow = null;
    for (const h of flow.handles) await h.remove();
    flow.handles = [];
  }
}
