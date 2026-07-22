import { Injectable, inject, effect } from '@angular/core';
import { ModalController } from '@ionic/angular/standalone';
import { ApprovalService } from './approval.service';
import { ApprovalSheetPage } from '../../features/approvals/approval-sheet.page';

/**
 * Auto-presents the app-global approval sheet (ApprovalSheetPage) whenever an out-of-band approval
 * awaits, and dismisses it once none remain. Instantiate once at startup (injected in app.config).
 * Mirrors JoinPromptService — the overlay itself reads `approval.pending()` live, so it updates as
 * requests arrive or resolve; this only opens/closes the container.
 */
@Injectable({ providedIn: 'root' })
export class ApprovalPromptService {
  private readonly approval = inject(ApprovalService);
  private readonly modalCtrl = inject(ModalController);
  private modal: HTMLIonModalElement | null = null;
  private busy = false;

  constructor() {
    effect(() => {
      const has = this.approval.has();
      if (has && !this.modal && !this.busy) void this.open();
      else if (!has && this.modal) void this.close();
    });
  }

  private async open(): Promise<void> {
    this.busy = true;
    try {
      this.modal = await this.modalCtrl.create({
        component: ApprovalSheetPage,
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
