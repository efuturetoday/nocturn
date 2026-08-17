import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonHeader,
  IonToolbar,
  IonButtons,
  IonButton,
  IonMenuButton,
  ActionSheetController,
} from '@ionic/angular/standalone';
import { LucideMenu, LucideChevronDown } from '@lucide/angular';
import { WorkspaceService } from '../core/services/workspace.service';

/**
 * The shared header for the shell's plain pages. The menu button leads on the left — it is how you
 * leave any page, so it holds the position the thumb reaches for first — and the workspace SWITCHER
 * sits on the right: the active workspace name plus a chevron so it reads as tappable.
 *
 * The switcher lives in a buttons slot rather than inside ion-title: a centred ion-title is not
 * reliably clickable in ios mode, and the whole point of this title is that you can tap it.
 */
@Component({
  selector: 'app-workspace-header',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonHeader, IonToolbar, IonButtons, IonButton, IonMenuButton, LucideMenu, LucideChevronDown],
  template: `
    <ion-header>
      <ion-toolbar>
        <ion-buttons slot="start">
          <ion-menu-button menu="main" aria-label="Open menu"><svg lucideMenu [size]="24" /></ion-menu-button>
        </ion-buttons>
        <ion-buttons slot="end">
          <ion-button class="ws-switch" (click)="choose()" aria-label="Switch workspace">
            <span class="ws-name">{{ ws.activeTitle() }}</span>
            <svg lucideChevronDown [size]="16" class="caret" />
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
    .ws-switch .caret { color: var(--ion-color-medium); }
  `,
})
export class WorkspaceHeaderComponent {
  protected readonly ws = inject(WorkspaceService);
  private readonly sheets = inject(ActionSheetController);
  private readonly router = inject(Router);

  protected async choose(): Promise<void> {
    const current = this.ws.active();
    const sheet = await this.sheets.create({
      header: 'Workspace',
      // Tapping a name switches immediately — no separate confirm step. The active one is marked
      // selected so it reads as the current choice. The sheet lists TITLES: it is the switcher, and
      // the folder name belongs on the page where it can be explained.
      //
      // "Manage" sits at the bottom because this is where someone looking for it arrives: the moment
      // you notice a workspace is missing is the moment you opened the switcher.
      buttons: [
        ...this.ws.workspaces().map((w) => ({
          text: w.title,
          role: w.name === current ? ('selected' as const) : undefined,
          handler: () => {
            void this.ws.setActive(w.name);
          },
        })),
        {
          text: 'Manage workspaces…',
          handler: () => {
            void this.router.navigateByUrl('/app/workspaces');
          },
        },
        { text: 'Cancel', role: 'cancel' as const },
      ],
    });
    await sheet.present();
  }
}
