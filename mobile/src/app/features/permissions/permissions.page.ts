import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import {
  IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote, IonButton, IonChip,
  AlertController,
} from '@ionic/angular/standalone';
import { GrantService } from '../../core/services/grant.service';
import type { GrantInfo } from '../../core/protocol/nocturn-protocol';

/**
 * What this workspace may do without asking again — and how to take it back.
 *
 * The gate asks on a new action and remembers the answer, which is right for the asking and says
 * nothing about the years after. A remembered approval records what, never why: once whatever
 * prompted the question is gone, the answer stands on its own. This page exists because a permission
 * nobody can see is a permission nobody revokes.
 *
 * Two things every row has to carry. The KIND, because "net" and "file" are different powers and the
 * target alone does not say which. And whether it is DURABLE: a session grant lapses when the daemon
 * stops, a durable one is written down, and only the second accumulates.
 */
@Component({
  selector: 'app-permissions',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote, IonButton, IonChip],
  template: `
    <ion-content>
      <ion-list inset="true">
        <ion-list-header><ion-label>Standing permissions</ion-label></ion-list-header>
        @for (g of grants.grants(); track g.kind + ':' + g.target) {
          <ion-item lines="full">
            <ion-label class="ion-text-wrap">
              <h2>{{ g.target }}</h2>
              <ion-note>{{ g.kind }} · {{ g.durable ? 'remembered' : 'until the daemon restarts' }}</ion-note>
            </ion-label>
            @if (g.durable) {
              <ion-chip slot="end" color="medium" outline="true">always</ion-chip>
            }
            <ion-button slot="end" fill="clear" color="danger" (click)="revoke(g)">Revoke</ion-button>
          </ion-item>
        } @empty {
          <ion-item lines="none">
            <ion-label color="medium">
              Nothing standing. Every action still asks.
            </ion-label>
          </ion-item>
        }
      </ion-list>

      <p class="hint">
        Revoking is safe: the next action of that shape asks you again, which is how the gate works
        anyway. What it costs is one more question, once.
      </p>
    </ion-content>
  `,
  styles: `
    .hint {
      max-width: min(var(--nocturn-measure), calc(100% - 2rem));
      margin-inline: auto;
      padding: 0 0.25rem 1rem;
      color: var(--ion-color-medium);
      font-size: 0.85rem;
    }
  `,
})
export class PermissionsPage {
  protected readonly grants = inject(GrantService);
  private readonly alerts = inject(AlertController);

  /**
   * Behind a confirmation, and the message says what it costs rather than warning in the abstract:
   * a person revoking one has to know they will be asked again, not fear they broke something.
   */
  protected async revoke(g: GrantInfo): Promise<void> {
    const alert = await this.alerts.create({
      header: `Revoke ${g.target}?`,
      message:
        `The assistant will ask you again the next time it wants to reach it. If something is ` +
        `running right now, that is when the question arrives.`,
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        { text: 'Revoke', role: 'destructive', handler: () => this.grants.forget(g.kind, g.target) },
      ],
    });
    await alert.present();
  }
}
