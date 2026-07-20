import { Injectable, inject, effect } from '@angular/core';
import { Router } from '@angular/router';
import { Capacitor } from '@capacitor/core';
import { PushNotifications } from '@capacitor/push-notifications';
import { ConnectionService } from './connection.service';
import { AuthService } from './auth.service';
import { WorkspaceService } from './workspace.service';

/**
 * PushService registers this device's native push token with the daemon so it can wake the app
 * out-of-band (APNs on iOS): a background approval, a proactive notify, or "your answer is ready"
 * on a chat's turnEnd. Once connected+paired it asks for push permission and registers; the OS
 * `registration` event carries the token → POST /register (AuthService). It also deep-links a
 * tapped notification to the exact chat (payload carries `ws`+`chatId`). Native only — no-op on
 * the web.
 */
@Injectable({ providedIn: 'root' })
export class PushService {
  private readonly conn = inject(ConnectionService);
  private readonly auth = inject(AuthService);
  private readonly workspaces = inject(WorkspaceService);
  private readonly router = inject(Router);
  private asked = false;

  constructor() {
    if (!Capacitor.isNativePlatform()) return;

    void PushNotifications.addListener('registration', (t) => {
      const url = this.conn.currentUrl();
      if (url) void this.auth.registerPush(url, t.value);
    });
    void PushNotifications.addListener('registrationError', () => {
      /* leave push off; approvals still reach an attended (foreground) device in-band */
    });

    // A tapped notification deep-links to its chat. The route's ChatPage opens the chat itself;
    // ChatService's resync effect re-opens it once the connection is up, so a cold-start tap
    // (app was killed) still lands in the right chat after it connects.
    void PushNotifications.addListener('pushNotificationActionPerformed', (a) => {
      const d = a.notification.data as { chatId?: string; ws?: string } | undefined;
      if (d?.chatId) void this.deepLink(d.ws, d.chatId);
    });

    // Ask + register once we have a paired connection (bearer available for POST /register).
    effect(() => {
      if (this.conn.state() === 'connected' && !this.asked) {
        this.asked = true;
        void this.enable();
      }
    });
  }

  private async deepLink(ws: string | undefined, chatId: string): Promise<void> {
    if (ws) await this.workspaces.setActive(ws);
    await this.router.navigate(['/tabs', 'chat', chatId]);
  }

  private async enable(): Promise<void> {
    let perm = await PushNotifications.checkPermissions();
    if (perm.receive === 'prompt' || perm.receive === 'prompt-with-rationale') {
      perm = await PushNotifications.requestPermissions();
    }
    if (perm.receive === 'granted') await PushNotifications.register();
  }
}
