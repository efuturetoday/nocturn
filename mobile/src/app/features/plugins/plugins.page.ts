import { Component, ChangeDetectionStrategy, inject, signal } from '@angular/core';
import {
  IonContent,
  IonList,
  IonListHeader,
  IonItem,
  IonLabel,
  IonNote,
  IonButton,
  AlertController,
} from '@ionic/angular/standalone';
import { LucideStore } from '@lucide/angular';
import { PluginService } from '../../core/services/plugin.service';
import { LibraryModalComponent } from '../library/library-modal';
import type { PluginInfo } from '../../core/protocol/nocturn-protocol';

/**
 * The active workspace's plugins: what is installed, and how many tools each one contributed.
 *
 * Deliberately thinner than the skills page, and the difference is the point. A skill can be switched
 * off and read in full here because it is text this household owns; a plugin's manifest is shown by
 * the Library BEFORE installing, which is where the decision is actually made. What is left for this
 * page is the inventory and the one destructive act — and that act takes the plugin's standing
 * permissions with it, which is what makes it safe to offer here at all.
 */
@Component({
  selector: 'app-plugins',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    LibraryModalComponent,
    LucideStore,
    IonContent,
    IonList,
    IonListHeader,
    IonItem,
    IonLabel,
    IonNote,
    IonButton,
  ],
  template: `
    <ion-content>
      <ion-list inset="true">
        <ion-list-header><ion-label>Plugins</ion-label></ion-list-header>
        @for (p of plugins.plugins(); track p.name) {
          <ion-item lines="full">
            <ion-label>
              <h2>{{ p.name }}</h2>
              <ion-note>{{ toolCount(p) }}</ion-note>
            </ion-label>
            <ion-button slot="end" fill="clear" color="danger" (click)="remove(p)">
              Remove
            </ion-button>
          </ion-item>
        } @empty {
          <ion-item lines="none">
            <ion-label color="medium">
              No plugins here yet. Browse the Library below, or drop a folder into this workspace's
              <code>plugins/</code>.
            </ion-label>
          </ion-item>
        }

        <ion-item button lines="none" (click)="browsing.set(true)">
          <svg lucideStore slot="start" [size]="21" class="add" />
          <ion-label color="primary">Browse the Library…</ion-label>
        </ion-item>
      </ion-list>

      <p class="hint">
        A plugin's tools reach nothing on their own: every effect still asks you, and a plugin that
        needs an account stays inert until one is connected on the machine running Nocturn.
      </p>

      <app-library-modal [(open)]="browsing" initial="plugin" />
    </ion-content>
  `,
  styles: `
    .add { color: var(--ion-color-primary); }
    .hint {
      max-width: min(var(--nocturn-measure), calc(100% - 2rem));
      margin-inline: auto;
      padding: 0 0.25rem 1rem;
      color: var(--ion-color-medium);
      font-size: 0.85rem;
    }
  `,
})
export class PluginsPage {
  protected readonly plugins = inject(PluginService);
  private readonly alerts = inject(AlertController);

  protected readonly browsing = signal(false);

  protected toolCount(p: PluginInfo): string {
    return p.tools === 1 ? '1 tool' : `${p.tools} tools`;
  }

  /**
   * Delete the plugin, behind a confirmation that says what goes with it. The permission half is
   * worth spelling out: a grant records what, never why, so one left standing for a program that is
   * gone would be inherited by the next thing to reach that host.
   */
  protected async remove(p: PluginInfo): Promise<void> {
    const alert = await this.alerts.create({
      header: `Remove ${p.name}?`,
      message:
        `Its folder is deleted, and any account connected for it goes with it. The remembered ` +
        `permission to reach its hosts is revoked too — if you had allowed one of them for something ` +
        `else, Nocturn will ask about it once more. You can install it again from the Library.`,
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        { text: 'Remove', role: 'destructive', handler: () => this.plugins.remove(p.name) },
      ],
    });
    await alert.present();
  }
}
