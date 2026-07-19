import { Injectable, inject, effect } from '@angular/core';
import { ModalController } from '@ionic/angular/standalone';
import { AuthService } from './auth.service';
import { JoinsPage } from '../../features/joins/joins.page';

/**
 * Auto-presents the pairing-request reveal overlay (JoinsPage) — the SAME sheet used for pairing
 * input — whenever a device asks to join, and dismisses it once no requests remain. Instantiate
 * once at startup (injected in App). The overlay itself reads `auth.joins()` live.
 */
@Injectable({ providedIn: 'root' })
export class JoinPromptService {
  private readonly auth = inject(AuthService);
  private readonly modalCtrl = inject(ModalController);
  private modal: HTMLIonModalElement | null = null;
  private busy = false;

  constructor() {
    effect(() => {
      const has = this.auth.joins().length > 0;
      if (has && !this.modal && !this.busy) void this.open();
      else if (!has && this.modal) void this.close();
    });
  }

  private async open(): Promise<void> {
    this.busy = true;
    try {
      this.modal = await this.modalCtrl.create({
        component: JoinsPage,
        breakpoints: [0, 0.5, 0.9],
        initialBreakpoint: 0.5,
        handle: true,
      });
      await this.modal.present();
      void this.modal.onDidDismiss().then(() => (this.modal = null));
    } finally {
      this.busy = false;
    }
  }

  private async close(): Promise<void> {
    const m = this.modal;
    this.modal = null;
    await m?.dismiss();
  }
}
