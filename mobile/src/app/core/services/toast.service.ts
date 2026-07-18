import { Injectable, inject } from '@angular/core';
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
    // Server-reported control errors → warning toast. Connection state is shown by the pill
    // above the tab bar, not a toast.
    this.conn.onEvent((e) => {
      if (e.type === 'error') void this.show(e.text, 'danger');
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
