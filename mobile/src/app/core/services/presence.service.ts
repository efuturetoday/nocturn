import { Injectable, inject, effect } from '@angular/core';
import { Capacitor } from '@capacitor/core';
import { App } from '@capacitor/app';
import { ConnectionService } from './connection.service';

/**
 * PresenceService reports foreground/background to the daemon (`setPresence`), which drives HITL
 * routing: while this device is active, approvals come in-band over the WebSocket (inline prompt /
 * `approvalPending` badge); when it backgrounds, the daemon pushes them out-of-band instead. Uses
 * @capacitor/app's appStateChange on native, document visibility on the web. Re-asserts the current
 * state on (re)connect (a fresh connection is assumed active, so this mainly re-sends `false`).
 */
@Injectable({ providedIn: 'root' })
export class PresenceService {
  private readonly conn = inject(ConnectionService);
  private active = true;

  constructor() {
    if (Capacitor.isNativePlatform()) {
      void App.addListener('appStateChange', ({ isActive }) => this.set(isActive));
    } else {
      document.addEventListener('visibilitychange', () => this.set(!document.hidden));
    }
    // Re-assert presence whenever a connection (re)establishes.
    effect(() => {
      if (this.conn.state() === 'connected') this.conn.send({ cmd: 'setPresence', active: this.active });
    });
  }

  private set(active: boolean): void {
    this.active = active;
    // Coming to the foreground: force an immediate reconnect (the connect effect re-asserts
    // presence once the socket opens). Otherwise just report the new state.
    if (active) this.conn.reconnectNow();
    this.conn.send({ cmd: 'setPresence', active });
  }
}
