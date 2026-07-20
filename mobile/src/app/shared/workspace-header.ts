import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import {
  IonHeader, IonToolbar, IonButtons, IonButton, IonIcon, AlertController,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { chevronDownOutline } from 'ionicons/icons';
import { WorkspaceService } from '../core/services/workspace.service';

/**
 * The shared tab-root header: a workspace SWITCHER as the title — the active workspace name plus a
 * chevron so it reads as tappable (opens the chooser). The bottom tab bar names the page, so no
 * page title is shown; connection status lives in a pill above the tab bar.
 */
@Component({
  selector: 'app-workspace-header',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonHeader, IonToolbar, IonButtons, IonButton, IonIcon],
  template: `
    <ion-header>
      <ion-toolbar>
        <ion-buttons slot="start">
          <ion-button class="ws-switch" (click)="choose()" aria-label="Switch workspace">
            <span class="ws-name">{{ ws.active() }}</span>
            <ion-icon slot="end" name="chevron-down-outline" aria-hidden="true" />
          </ion-button>
        </ion-buttons>
      </ion-toolbar>
    </ion-header>
  `,
  styles: `
    .ws-switch {
      --color: var(--ion-text-color);
      --padding-start: 0.5rem; --padding-end: 0.5rem;
      font-weight: 700; text-transform: none;
    }
    .ws-switch .ws-name { margin-right: 0.25rem; font-family: var(--font-display); }
    .ws-switch ion-icon[slot='end'] { color: var(--ion-color-medium); font-size: 0.85rem; }
  `,
})
export class WorkspaceHeaderComponent {
  protected readonly ws = inject(WorkspaceService);
  private readonly alerts = inject(AlertController);

  constructor() {
    addIcons({ chevronDownOutline });
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
