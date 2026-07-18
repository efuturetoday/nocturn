import { Injectable, inject, effect } from '@angular/core';
import { ToastController } from '@ionic/angular/standalone';
import { ConnectionService } from './connection.service';

/**
 * ToastService is the app's one place for transient feedback. It listens to the connection's
 * server `error` events and connection-state changes and shows an Ionic toast. Instantiate it
 * once at startup (injected in App) so it starts listening — nothing else needs to call it,
 * though `show()` is public for ad-hoc messages.
 */
@Injectable({ providedIn: 'root' })
export class ToastService {
  private readonly conn = inject(ConnectionService);
  private readonly toasts = inject(ToastController);

  constructor() {
    // Server-reported control errors → warning toast.
    this.conn.onEvent((e) => {
      if (e.type === 'error') void this.show(e.text, 'danger');
    });

    // Connection loss → a brief notice (skip the initial 'disconnected').
    let seen = false;
    effect(() => {
      const state = this.conn.state();
      if (state === 'reconnecting' && seen) void this.show('Connection lost — reconnecting…', 'warning');
      if (state === 'connected') seen = true;
    });
  }

  async show(message: string, color: 'danger' | 'warning' | 'success' | 'medium' = 'medium'): Promise<void> {
    const toast = await this.toasts.create({
      message,
      color,
      duration: color === 'danger' ? 4000 : 2500,
      position: 'top',
      buttons: [{ icon: 'close', role: 'cancel' }],
    });
    await toast.present();
  }
}
