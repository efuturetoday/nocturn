import { Component, ChangeDetectionStrategy, model, input } from '@angular/core';
import {
  IonModal, IonHeader, IonToolbar, IonTitle, IonButtons, IonButton,
} from '@ionic/angular/standalone';
import { LucideX } from '@lucide/angular';
import { LibraryBrowserComponent } from './library-browser';
import type { LibraryKind } from './library-filter';

/** The store as a dialog, opened from Skills and MCP. One wrapper so the sizing lives in one place. */
@Component({
  selector: 'app-library-modal',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [LibraryBrowserComponent, LucideX, IonModal, IonHeader, IonToolbar, IonTitle, IonButtons, IonButton],
  template: `
    <ion-modal class="library-dialog" [isOpen]="open()" (didDismiss)="open.set(false)">
      <ng-template>
        <ion-header>
          <ion-toolbar>
            <ion-title>Library</ion-title>
            <ion-buttons slot="end">
              <ion-button (click)="open.set(false)" aria-label="Close">
                <svg lucideX [size]="22" />
              </ion-button>
            </ion-buttons>
          </ion-toolbar>
        </ion-header>
        <app-library-browser [initial]="initial()" />
      </ng-template>
    </ion-modal>
  `,
  styles: `
    /* Only the width. Ionic already makes a modal full-screen on a phone and a centred card from md
       up, and it owns that threshold; overriding the height too turned the phone case into a card
       floating in the middle of a tall screen. Widening is all this needs. */
    .library-dialog {
      --width: min(72rem, 100vw);
    }
  `,
})
export class LibraryModalComponent {
  /** Two-way: `[(open)]="browsing"`. The sheet closes itself on a backdrop dismiss or the X, so the
      caller's signal follows without a handler. */
  readonly open = model(false);

  /** Which filter the store lands on — the page that opened it already knows what is wanted. */
  readonly initial = input<LibraryKind>('all');
}
