import { Injectable, effect, inject } from '@angular/core';
import { Capacitor } from '@capacitor/core';
import { PushNotifications } from '@capacitor/push-notifications';
import { AuthService } from './auth.service';
import { ConnectionService } from './connection.service';
import { NotificationService } from './notification.service';
import { isDemoUrl } from '../demo/is-demo';

/**
 * PushService registers this device's native APNs token with the daemon so it can be woken while
 * backgrounded. A push is only a WAKE and carries no authority — no approval token ever rides it.
 * Native platforms only (the browser has no token).
 *
 * Flow: once connected, ask for permission and `register()`; the OS answers with a token on the
 * `registration` event, which we POST to `/register` (bearer-authed) via AuthService.
 *
 * A tap is routed by the push's `type`:
 *   • approval — nothing to do here. Foregrounding reconnects, and the daemon re-presents the
 *     pending approval over the WebSocket, which raises the overlay on its own.
 *   • remind / notify — open what it came from, via the same path the in-app toast uses.
 */
@Injectable({ providedIn: 'root' })
export class PushService {
  private readonly auth = inject(AuthService);
  private readonly conn = inject(ConnectionService);
  private readonly notifications = inject(NotificationService);
  private started = false;

  constructor() {
    if (!Capacitor.isNativePlatform()) return;

    PushNotifications.addListener('registration', (t) => {
      const url = this.conn.currentUrl();
      if (url) void this.auth.registerPush(url, t.value);
    });

    PushNotifications.addListener('pushNotificationActionPerformed', (a) => {
      const data = (a.notification.data ?? {}) as { type?: string; ws?: string; chatId?: string };
      if (data.type !== 'remind' && data.type !== 'notify') return; // approvals re-present themselves
      if (!data.ws) return;
      void this.notifications.openTarget(data.ws, data.chatId);
    });

    // Register once per session, the first time we're connected (a bearer exists by then).
    effect(() => {
      if (this.conn.connected()) void this.ensure();
    });
  }

  private async ensure(): Promise<void> {
    if (this.started) return;
    // No daemon means nothing can ever wake this device, so asking for the permission would be a
    // prompt with no payoff — and in the demo it would be the first thing a reviewer sees.
    if (isDemoUrl(this.conn.currentUrl())) return;
    this.started = true;
    const perm = await PushNotifications.requestPermissions();
    if (perm.receive === 'granted') await PushNotifications.register();
  }
}
