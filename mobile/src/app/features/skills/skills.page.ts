import { Component, ChangeDetectionStrategy, inject, signal, computed } from '@angular/core';
import {
  IonContent,
  IonList,
  IonListHeader,
  IonItem,
  IonLabel,
  IonNote,
  IonToggle,
  IonButton,
  IonModal,
  IonHeader,
  IonToolbar,
  IonTitle,
  IonButtons,
  IonSpinner,
  AlertController,
} from '@ionic/angular/standalone';
import { LucideX, LucideStore } from '@lucide/angular';
import { SkillService } from '../../core/services/skill.service';
import { MarkdownComponent } from '../../shared/markdown';
import { LibraryModalComponent } from '../library/library-modal';
import type { SkillInfo } from '../../core/protocol/nocturn-protocol';

/**
 * The active workspace's skills: switch one off, read what it actually says, or delete it.
 *
 * Two facts the rows have to carry, because getting either wrong changes what you expect a button to
 * do. The small name under the title is the ADDRESS and the folder beside it is only where it lives —
 * skills are the one place in the tree where the folder is not the identity. And OFF IS NOT GONE: the
 * folder moves aside, the assistant stops seeing it, everything shipped with it stays. That is why
 * the switch and the deletion are two different controls with two different weights.
 *
 * Delete is a button rather than a swipe, as on the workspaces page and for the same measured
 * reason: this bundle is also the browser UI, where a mouse drag on a sliding item resolves as a tap
 * and the row's own tap wins.
 */
@Component({
  selector: 'app-skills',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    MarkdownComponent,
    LibraryModalComponent,
    LucideX,
    LucideStore,
    IonContent,
    IonList,
    IonListHeader,
    IonItem,
    IonLabel,
    IonNote,
    IonToggle,
    IonButton,
    IonModal,
    IonHeader,
    IonToolbar,
    IonTitle,
    IonButtons,
    IonSpinner,
  ],
  template: `
    <ion-content>
      <ion-list inset="true">
        <ion-list-header><ion-label>Skills</ion-label></ion-list-header>
        @for (s of skills.skills(); track s.name) {
          <ion-item button lines="full" (click)="open(s)" [class.off]="!s.enabled">
            <ion-label>
              <h2>{{ s.name }}</h2>
              @if (s.description) {
                <p>{{ s.description }}</p>
              }
              <ion-note>{{ subtitle(s) }}</ion-note>
            </ion-label>
            <!-- Both controls stop the click: the row itself opens the skill, and a toggle that also
                 opened it would leave a sheet in front of the switch you just flipped. -->
            @if (s.plugin) {
              <!-- A bundled skill has no folder of its own, so there is nothing here to switch off or
                   delete: it arrives and leaves with its plugin. Shown anyway, because it is in front
                   of the model. -->
              <ion-note slot="end">from {{ s.plugin }}</ion-note>
            } @else {
              <ion-toggle
                slot="end"
                [checked]="s.enabled"
                (click)="$event.stopPropagation()"
                (ionChange)="toggle(s, $event)"
                [attr.aria-label]="'Enable ' + s.name"
              />
              <ion-button slot="end" fill="clear" color="danger" (click)="$event.stopPropagation(); remove(s)">
                Delete
              </ion-button>
            }
          </ion-item>
        } @empty {
          <ion-item lines="none">
            <ion-label color="medium">
              No skills here yet. Browse the Library below, or drop a folder into this workspace's
              <code>skills/</code>.
            </ion-label>
          </ion-item>
        }

        <ion-item button lines="none" (click)="browsing.set(true)">
          <svg lucideStore slot="start" [size]="21" class="add" />
          <ion-label color="primary">Browse the Library…</ion-label>
        </ion-item>
      </ion-list>

      <!--
        Not a delay, a rule: the model is handed its tool list when a turn starts and plans against
        it, so a tool may not disappear between two calls of the same turn. Saying so is the
        difference between "this is deliberate" and "this is broken".
      -->
      <p class="hint">
        A change takes effect on the next message. A reply that is already running keeps the skills
        it started with.
      </p>

      <app-library-modal [(open)]="browsing" initial="skill" />

      <ion-modal [isOpen]="reading() !== null" (didDismiss)="reading.set(null)">
        <ng-template>
          <ion-header>
            <ion-toolbar>
              <ion-title>{{ reading() }}</ion-title>
              <ion-buttons slot="end">
                <ion-button (click)="reading.set(null)" aria-label="Close">
                  <svg lucideX [size]="22" />
                </ion-button>
              </ion-buttons>
            </ion-toolbar>
          </ion-header>
          <ion-content class="body-window">
            <!-- Verbatim, frontmatter and all: the reason to open a skill is to see exactly what the
                 model is told, and a prettier rendering that dropped the preamble would hide the
                 half that decides when it is loaded. -->
            @if (body(); as text) {
              <app-markdown [text]="text" />
            } @else {
              <ion-spinner name="dots" />
            }
          </ion-content>
        </ng-template>
      </ion-modal>
    </ion-content>
  `,
  styles: `
    /* A disabled skill is still a row, only quieter — it has to stay readable enough to switch back on. */
    .off ion-label { opacity: 0.55; }
    .add { color: var(--ion-color-primary); }
    ion-toggle { padding-inline-end: 0.25rem; }
    .body-window { --padding-start: 1rem; --padding-end: 1rem; --padding-top: 0.5rem; }
    .hint {
      max-width: min(var(--nocturn-measure), calc(100% - 2rem));
      margin-inline: auto;
      margin-block: 0;
      color: var(--ion-color-medium);
      font-size: 0.85rem;
    }
  `,
})
export class SkillsPage {
  protected readonly skills = inject(SkillService);
  private readonly alerts = inject(AlertController);

  protected readonly browsing = signal(false);

  /** The skill whose SKILL.md is open, by name. */
  protected readonly reading = signal<string | null>(null);
  protected readonly body = computed(() => {
    const name = this.reading();
    return name === null ? null : (this.skills.bodies()[name] ?? null);
  });

  /** The line under a skill: where it lives, how big it is, and whether it is off. */
  protected subtitle(s: SkillInfo): string {
    const size = s.bytes < 1024 ? `${s.bytes} B` : `${Math.round(s.bytes / 1024)} kB`;
    const where = s.folder === s.name ? size : `${s.folder}/ · ${size}`;
    return s.enabled ? where : `${where} · off`;
  }

  protected open(s: SkillInfo): void {
    this.reading.set(s.name);
    this.skills.read(s.name);
  }

  protected toggle(s: SkillInfo, ev: Event): void {
    const on = (ev as CustomEvent<{ checked: boolean }>).detail.checked;
    if (on === s.enabled) return; // the daemon's own broadcast re-rendered us; not a user action
    this.skills.enable(s.name, on);
  }

  /**
   * Delete a skill, behind a confirmation.
   *
   * This one has no trash, unlike a workspace — so the reassurance has to be the true one: anything
   * that came from the catalog can be installed again, and switching off is there for the case where
   * that is not certain.
   */
  protected async remove(s: SkillInfo): Promise<void> {
    const alert = await this.alerts.create({
      header: `Delete ${s.name}?`,
      message:
        `Its folder is deleted — there is no trash for skills. Anything from the Library can be ` +
        `installed again; to keep it without the assistant seeing it, switch it off instead.`,
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        { text: 'Delete', role: 'destructive', handler: () => this.skills.remove(s.name) },
      ],
    });
    await alert.present();
  }
}
