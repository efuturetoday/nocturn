import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import {
  IonContent,
  IonList,
  IonListHeader,
  IonItem,
  IonLabel,
  IonItemSliding,
  IonItemOptions,
  IonItemOption,
} from '@ionic/angular/standalone';
import { LucideAlarmClock } from '@lucide/angular';
import { ReminderService } from '../../core/services/reminder.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import { ReminderRowComponent } from './components/reminder-row';

/**
 * Pending reminders. Swipe a row to cancel; there is no create — the model sets them, which is why
 * the empty state says so rather than offering a button that does not exist.
 *
 * As a dashboard strip this section simply vanished when nothing was pending. As a destination it
 * cannot: arriving at a blank page reads as a failure to load.
 */
@Component({
  selector: 'app-reminders',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    WorkspaceHeaderComponent,
    ReminderRowComponent,
    LucideAlarmClock,
    IonContent,
    IonList,
    IonListHeader,
    IonItem,
    IonLabel,
    IonItemSliding,
    IonItemOptions,
    IonItemOption,
  ],
  template: `
    <app-workspace-header />

    <ion-content>
      <ion-list inset="true">
        <ion-list-header><ion-label>Reminders</ion-label></ion-list-header>
        @for (r of reminders.reminders(); track r.id) {
          <ion-item-sliding>
            <ion-item lines="full">
              <svg lucideAlarmClock slot="start" [size]="21" class="due" />
              <app-reminder-row [reminder]="r" />
            </ion-item>
            <ion-item-options side="end">
              <ion-item-option color="danger" (click)="cancel(r.id)">Cancel</ion-item-option>
            </ion-item-options>
          </ion-item-sliding>
        } @empty {
          <ion-item lines="none">
            <ion-label color="medium">Nothing pending. Nocturn sets these when you ask it to.</ion-label>
          </ion-item>
        }
      </ion-list>

      @if (reminders.count()) {
        <p class="hint">Swipe a row to cancel it.</p>
      }
    </ion-content>
  `,
  styles: `
    .due { color: var(--ion-color-primary); }
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
export class RemindersPage {
  protected readonly reminders = inject(ReminderService);

  /** Cancel is not optimistic: the daemon broadcasts the change and the list refreshes from it. */
  protected cancel(id: string): void {
    this.reminders.cancel(id);
  }
}
