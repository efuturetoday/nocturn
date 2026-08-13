import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import {
  IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote, IonChip, IonButton,
  AlertController,
} from '@ionic/angular/standalone';
import { LucidePlus } from '@lucide/angular';
import { WorkspaceService } from '../../core/services/workspace.service';
import type { WorkspaceInfo } from '../../core/protocol/nocturn-protocol';

/** The daemon's own rule (discovery.ValidName). Checked here so a typo is answered by the field that
    holds it rather than by a toast a second later — the daemon still validates, this is a courtesy. */
const VALID_NAME = /^[a-z0-9][a-z0-9_-]*$/;

/**
 * Manage the household's workspaces.
 *
 * The page exists to make one distinction visible: the row's TITLE is a label, the small name under
 * it is the identity — the folder on disk, the input to that workspace's vault key, and the address
 * on every other command. Renaming moves the title only, which is why both are on the row: a screen
 * that showed one name would leave you expecting the other to follow it.
 *
 * Delete is a visible button rather than a swipe action, unlike the reminders list it is otherwise
 * shaped like. This bundle is ALSO the browser UI, and a swipe there is not a gesture anyone has:
 * a mouse drag on a sliding item resolves as a tap, so the row's own tap-to-rename wins and the
 * delete is simply unreachable. The devices list already answers this shape (settings.page.ts).
 *
 * Nothing here is optimistic and nothing is pre-disabled by capability. A device without `manage`
 * learns so from the daemon's error toast, because the device class is deliberately never on the
 * wire — a value the client controls is not a fact about the client.
 */
@Component({
  selector: 'app-workspaces',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    LucidePlus,
    IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote, IonChip, IonButton,
  ],
  template: `
    <ion-content>
      <ion-list inset="true">
        <ion-list-header><ion-label>Workspaces</ion-label></ion-list-header>
        @for (w of ws.workspaces(); track w.name) {
          <ion-item button lines="full" (click)="rename(w)">
            <ion-label>
              <h2>{{ w.title }}</h2>
              <ion-note>{{ w.name }}</ion-note>
            </ion-label>
            @if (w.name === ws.active()) {
              <ion-chip slot="end" color="primary">Active</ion-chip>
            }
            @if (w.default) {
              <ion-chip slot="end" color="medium">Default</ion-chip>
            }
            <!-- The default workspace gets no Delete at all: it is recreated at startup, so a button
                 that always fails is worse than no button. -->
            @if (!w.default) {
              <!-- stopPropagation because the ROW is a button too: without it, Delete would also
                   open the rename dialog behind its own confirmation. -->
              <ion-button slot="end" fill="clear" color="danger" (click)="$event.stopPropagation(); remove(w)" [attr.aria-label]="'Delete ' + w.title">
                Delete
              </ion-button>
            }
          </ion-item>
        } @empty {
          <ion-item lines="none">
            <ion-label color="medium">Waiting for the daemon's list…</ion-label>
          </ion-item>
        }

        <ion-item button lines="none" (click)="create()">
          <svg lucidePlus slot="start" [size]="21" class="add" />
          <ion-label color="primary">New workspace…</ion-label>
        </ion-item>
      </ion-list>

      <p class="hint">Tap a workspace to rename it. Switch between them with the name at the top.</p>
    </ion-content>
  `,
  styles: `
    .add { color: var(--ion-color-primary); }
    /* Sits with the inset card, not with the page edge, so it reads as a footnote to the list. That
       means the card's OWN box (styles.scss centres inset lists on the measure), not a 1rem gutter:
       a fixed gutter is identical to it on a phone and drifts to the far left of a wide pane, where
       the footnote no longer belongs to anything. */
    .hint {
      max-width: min(var(--nocturn-measure), calc(100% - 2rem));
      margin-inline: auto;
      margin-block: 0;
      color: var(--ion-color-medium);
      font-size: 0.85rem;
    }
  `,
})
export class WorkspacesPage {
  protected readonly ws = inject(WorkspaceService);
  private readonly alerts = inject(AlertController);

  /**
   * Add a workspace. Two fields, name first: it is the one that cannot be taken back, so it is asked
   * for rather than derived from the title behind the user's back.
   */
  protected async create(): Promise<void> {
    const alert = await this.alerts.create({
      header: 'New workspace',
      message:
        'The name is permanent — it is the folder on disk and how every command addresses this ' +
        'workspace. The display name is only a label and can change any time.',
      inputs: [
        { name: 'name', type: 'text', placeholder: 'name (e.g. work)' },
        { name: 'title', type: 'text', placeholder: 'Display name (optional)' },
      ],
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        {
          text: 'Create',
          handler: (v) => {
            const name = (v.name ?? '').trim();
            if (!VALID_NAME.test(name)) return false;
            const title = (v.title ?? '').trim();
            this.ws.create(name, title || undefined);
            return true;
          },
        },
      ],
    });
    await alert.present();
  }

  /** Change the label. An empty title is allowed and resets it to the folder name. */
  protected async rename(w: WorkspaceInfo): Promise<void> {
    const alert = await this.alerts.create({
      header: 'Rename workspace',
      message: `The name stays ${w.name} — only the label changes. Leave it empty to show the name again.`,
      inputs: [{ name: 'title', type: 'text', value: w.title, placeholder: 'Display name' }],
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        { text: 'Rename', handler: (v) => { this.ws.rename(w.name, (v.title ?? '').trim()); } },
      ],
    });
    await alert.present();
  }

  /**
   * Remove a workspace, behind a confirmation that says what actually happens.
   *
   * The daemon moves the directory to a trash folder rather than deleting it, so "permanently
   * deleted" would be a lie — and the honest wording is also the reassuring one, which is the point:
   * this is a swipe on a list, on a phone.
   */
  protected async remove(w: WorkspaceInfo): Promise<void> {
    const alert = await this.alerts.create({
      header: `Delete ${w.title}?`,
      message:
        `It is moved to the trash folder beside your workspaces — its chats, notes and vault stay ` +
        `on disk under ${w.name}. Nocturn stops serving it immediately.`,
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        { text: 'Delete', role: 'destructive', handler: () => this.ws.remove(w.name) },
      ],
    });
    await alert.present();
  }
}
