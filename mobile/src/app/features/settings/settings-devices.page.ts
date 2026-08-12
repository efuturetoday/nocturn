import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import {
  IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote, IonChip, IonButton,
  AlertController,
} from '@ionic/angular/standalone';
import { AuthService } from '../../core/services/auth.service';
import type { EnrolledDevice } from '../../core/protocol/nocturn-protocol';

/** Who is paired with this household, and the way to un-pair them. */
@Component({
  selector: 'app-settings-devices',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote, IonChip, IonButton],
  template: `
    <ion-content>
      <ion-list inset="true">
        <ion-list-header><ion-label>Pairing requests</ion-label></ion-list-header>
        @for (j of auth.joins(); track j.joinId) {
          <ion-item>
            <ion-label>
              <h2>{{ j.name }}</h2>
              <ion-note>Share this code with the new device</ion-note>
            </ion-label>
            <ion-chip slot="end" color="primary">{{ j.code }}</ion-chip>
          </ion-item>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No pending requests.</ion-label></ion-item>
        }
      </ion-list>

      <!--
        The exit from "my phone is lost". Until this existed a bearer was valid until someone edited
        devices.json by hand and restarted the daemon — a remedy nobody finds at the moment they need
        it, and one that needs shell access to a machine they may be nowhere near.
      -->
      <ion-list inset="true">
        <ion-list-header><ion-label>Devices</ion-label></ion-list-header>
        @for (d of auth.devices(); track d.id) {
          <ion-item>
            <ion-label>
              <h2>{{ d.name }}</h2>
              <ion-note>{{ deviceSubtitle(d) }}</ion-note>
            </ion-label>
            @if (d.id === auth.selfId()) {
              <ion-chip slot="end" color="medium">This device</ion-chip>
            }
            <ion-button slot="end" fill="clear" color="danger" (click)="forget(d)">Forget</ion-button>
          </ion-item>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No devices.</ion-label></ion-item>
        }
      </ion-list>
    </ion-content>
  `,
})
export class SettingsDevicesPage {
  protected readonly auth = inject(AuthService);
  private readonly alerts = inject(AlertController);

  /** What a device is, in one line: its class and when it last connected. */
  protected deviceSubtitle(d: EnrolledDevice): string {
    const kind = { app: 'phone', web: 'browser', appliance: 'appliance', tool: 'command line' }[d.class ?? ''] ?? 'device';
    if (!d.lastUsed) return `${kind} · never connected`;
    return `${kind} · last seen ${new Date(d.lastUsed).toLocaleDateString()}`;
  }

  /**
   * Revoke a device, behind a confirmation.
   *
   * Irreversible in the only sense that matters — the bearer cannot be handed back, the device has to
   * pair again — so it is worth one tap. Forgetting THIS device is allowed and is how you sign a
   * browser out; the wording changes because the consequence does.
   */
  protected async forget(d: EnrolledDevice): Promise<void> {
    const self = d.id === this.auth.selfId();
    const alert = await this.alerts.create({
      header: self ? 'Sign out this device?' : `Forget ${d.name}?`,
      message: self
        ? 'This device will be signed out and will have to pair again.'
        : d.class === 'tool'
          // The command line is the one row that heals: the daemon writes it a fresh credential
          // straight away, so this rotates rather than revokes. Which is what you want from it — the
          // reason to do this is that the file leaked, and the copy that leaked stops working.
          ? 'The command line will be issued a new credential. Any copy of the old one stops working.'
          : `${d.name} will lose access immediately and will have to pair again.`,
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        { text: self ? 'Sign out' : 'Forget', role: 'destructive', handler: () => this.auth.forget(d.id) },
      ],
    });
    await alert.present();
  }
}
