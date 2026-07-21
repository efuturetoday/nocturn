import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import {
  IonHeader, IonToolbar, IonButtons, IonButton, IonIcon, ActionSheetController,
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
    /* Match ion-title typography so the switcher reads as the page title, not a small button. */
    .ws-switch .ws-name { margin-right: 0.25rem; font-family: var(--font-display); font-size: 1.25rem; }
    .ws-switch ion-icon[slot='end'] { color: var(--ion-color-medium); font-size: 1rem; }
  `,
})
export class WorkspaceHeaderComponent {
  protected readonly ws = inject(WorkspaceService);
  private readonly sheets = inject(ActionSheetController);

  constructor() {
    addIcons({ chevronDownOutline });
  }

  protected async choose(): Promise<void> {
    const current = this.ws.active();
    const sheet = await this.sheets.create({
      header: 'Workspace',
      // Tapping a name switches immediately — no separate confirm step. The active one is marked
      // selected so it reads as the current choice.
      buttons: [
        ...this.ws.workspaces().map((w) => ({
          text: w.name,
          role: w.name === current ? ('selected' as const) : undefined,
          handler: () => { void this.ws.setActive(w.name); },
        })),
        { text: 'Cancel', role: 'cancel' as const },
      ],
    });
    await sheet.present();
  }
}
