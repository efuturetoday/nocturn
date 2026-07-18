import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import {
  IonHeader, IonToolbar, IonTitle, IonButtons, IonButton, IonIcon, AlertController,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { swapHorizontalOutline } from 'ionicons/icons';
import { WorkspaceService } from '../core/services/workspace.service';

/**
 * The shared tab-root header: the active workspace NAME as the title, and a switch icon in the
 * end slot that opens a workspace chooser. The bottom tab bar names the page, so no page title
 * is shown; connection status lives in a pill above the tab bar, not here.
 */
@Component({
  selector: 'app-workspace-header',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonHeader, IonToolbar, IonTitle, IonButtons, IonButton, IonIcon],
  template: `
    <ion-header>
      <ion-toolbar>
        <ion-title>{{ ws.active() }}</ion-title>
        <ion-buttons slot="end">
          <ion-button (click)="choose()" aria-label="Switch workspace">
            <ion-icon slot="icon-only" name="swap-horizontal-outline" />
          </ion-button>
        </ion-buttons>
      </ion-toolbar>
    </ion-header>
  `,
})
export class WorkspaceHeaderComponent {
  protected readonly ws = inject(WorkspaceService);
  private readonly alerts = inject(AlertController);

  constructor() {
    addIcons({ swapHorizontalOutline });
  }

  protected async choose(): Promise<void> {
    const current = this.ws.active();
    const alert = await this.alerts.create({
      header: 'Workspace',
      inputs: this.ws.workspaces().map((w) => ({
        type: 'radio' as const,
        label: w.name,
        value: w.name,
        checked: w.name === current,
      })),
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        { text: 'Switch', handler: (name: string) => { if (name) void this.ws.setActive(name); } },
      ],
    });
    await alert.present();
  }
}
