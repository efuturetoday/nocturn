import { Component, ChangeDetectionStrategy, inject, signal } from '@angular/core';
import {
  IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote, IonButton, AlertController,
} from '@ionic/angular/standalone';
import { LucideStore } from '@lucide/angular';
import { PluginService } from '../../core/services/plugin.service';
import { LibraryModalComponent } from '../library/library-modal';
import type { PluginInfo } from '../../core/protocol/nocturn-protocol';

/**
 * The active workspace's plugins: what is installed, and how many tools each one contributed.
 *
 * Deliberately thinner than the skills page, and the difference is the point. A skill can be switched
 * off, read in full and deleted from here because it is text this household owns. A plugin is code
 * with a manifest, and the two things you would want here — reading what it asks for, and removing it
 * — belong elsewhere for now: the manifest is shown by the Library before installing, where the
 * decision is actually made, and removing has to revoke the remembered permission for the hosts its
 * credential rode to before it can be a button. Until it does, this page says where the folder is
 * rather than offering half of it.
 */
@Component({
  selector: 'app-plugins',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    LibraryModalComponent, LucideStore,
    IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote, IonButton,
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
            <ion-button slot="end" fill="clear" color="medium" (click)="howToRemove(p)">
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

  /** Removing is a host-side gesture until the grant revocation goes with it — see the class doc. */
  protected async howToRemove(p: PluginInfo): Promise<void> {
    const alert = await this.alerts.create({
      header: `Remove ${p.name}?`,
      message:
        `Not from here yet: removing a plugin also has to take back the permission you gave for the ` +
        `hosts its credential rode to, and half of that would leave a permission standing for a ` +
        `program that is gone. On the machine running Nocturn, delete plugins/${p.name} and run ` +
        `<code>nocturn reload</code>.`,
      buttons: [{ text: 'OK', role: 'cancel' }],
    });
    await alert.present();
  }
}
