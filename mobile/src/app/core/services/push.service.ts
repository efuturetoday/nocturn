import { Injectable, effect, inject } from '@angular/core';
import { Capacitor } from '@capacitor/core';
import { PushNotifications } from '@capacitor/push-notifications';
import { AuthService } from './auth.service';
import { ConnectionService } from './connection.service';

/**
 * PushService registers this device's native APNs token with the daemon so it can be woken for a
 * pending approval while backgrounded. A push is only a WAKE — the tap foregrounds the app, which
 * reconnects and re-presents the pending approval over the authenticated WebSocket; no approval
 * token ever rides the push. Native platforms only (the browser has no token).
 *
 * Flow: once connected, ask for permission and `register()`; the OS answers with a token on the
 * `registration` event, which we POST to `/register` (bearer-authed) via AuthService.
 */
@Injectable({ providedIn: 'root' })
export class PushService {
  private readonly auth = inject(AuthService);
  private readonly conn = inject(ConnectionService);
  private started = false;

  constructor() {
    if (!Capacitor.isNativePlatform()) return;

    PushNotifications.addListener('registration', (t) => {
      const url = this.conn.currentUrl();
      if (url) void this.auth.registerPush(url, t.value);
    });

    // Register once per session, the first time we're connected (a bearer exists by then).
    effect(() => {
      if (this.conn.connected()) void this.ensure();
    });
  }

  private async ensure(): Promise<void> {
    if (this.started) return;
    this.started = true;
    const perm = await PushNotifications.requestPermissions();
    if (perm.receive === 'granted') await PushNotifications.register();
  }
}
