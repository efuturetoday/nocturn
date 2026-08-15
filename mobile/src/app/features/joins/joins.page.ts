import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import {
  IonHeader,
  IonToolbar,
  IonTitle,
  IonContent,
  IonButtons,
  IonButton,
  IonList,
  IonItem,
  IonLabel,
  IonNote,
  IonChip,
  ModalController,
} from '@ionic/angular/standalone';
import { AuthService } from '../../core/services/auth.service';

/**
 * The pairing-request REVEAL overlay (same sheet style as the pairing input). Shown on an
 * already-paired device when another device wants to join: it lists each request + the code to
 * read out. Reads `auth.joins()` live — new requests appear and redeemed/expired ones vanish;
 * the presenter (JoinPromptService) auto-dismisses when the list empties.
 */
@Component({
  selector: 'app-joins',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    IonHeader,
    IonToolbar,
    IonTitle,
    IonContent,
    IonButtons,
    IonButton,
    IonList,
    IonItem,
    IonLabel,
    IonNote,
    IonChip,
  ],
  template: `
    <ion-header>
      <ion-toolbar>
        <ion-title>Pairing request</ion-title>
        <ion-buttons slot="end"><ion-button (click)="close()">Close</ion-button></ion-buttons>
      </ion-toolbar>
    </ion-header>

    <ion-content class="ion-padding">
      <p class="hint">A device wants to pair. Read its code out and enter it on that device.</p>
      <ion-list inset="true">
        @for (j of auth.joins(); track j.joinId) {
          <ion-item>
            <ion-label>
              <h2>{{ j.name }}</h2>
              @if (j.platform) { <ion-note>{{ j.platform }}</ion-note> }
            </ion-label>
            <ion-chip slot="end" color="primary">{{ j.code }}</ion-chip>
          </ion-item>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No pending requests.</ion-label></ion-item>
        }
      </ion-list>
    </ion-content>
  `,
  styles: `
    .hint { text-align: center; color: var(--ion-color-medium); margin: 0.25rem 0 0.75rem; }
    ion-chip { font-size: 1.1rem; font-weight: 700; letter-spacing: 0.08em; }
  `,
})
export class JoinsPage {
  protected readonly auth = inject(AuthService);
  private readonly modalCtrl = inject(ModalController);

  protected close(): void {
    void this.modalCtrl.dismiss();
  }
}
