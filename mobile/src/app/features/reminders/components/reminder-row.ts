import { Component, ChangeDetectionStrategy, input, computed } from '@angular/core';
import { IonNote } from '@ionic/angular/standalone';
import type { ReminderInfo } from '../../../core/protocol/nocturn-protocol';

/**
 * How long until an RFC3339 instant, phrased forward ("in 20m"). Reminders are always in the future,
 * so this is the mirror of chat-row's relativeTime. A reminder already past its moment (the daemon
 * fires within the second, but a list can be a beat stale) reads "now" rather than a negative.
 */
export function dueIn(iso: string): string {
  const at = new Date(iso).getTime();
  if (isNaN(at)) return '';
  const s = Math.round((at - Date.now()) / 1000);
  if (s < 60) return 'now';
  const m = Math.round(s / 60);
  if (m < 60) return `in ${m}m`;
  const h = Math.round(m / 60);
  if (h < 24) return `in ${h}h`;
  const d = Math.round(h / 24);
  if (d === 1) return 'tomorrow';
  if (d < 7) return `in ${d}d`;
  return new Date(at).toLocaleDateString();
}

/** The reminder's clock time — the relative phrase alone is too vague to act on ("in 8h" vs "07:00"). */
export function clockTime(iso: string): string {
  const at = new Date(iso);
  if (isNaN(at.getTime())) return '';
  return at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

/**
 * One pending-reminder row: the message on top, when it fires below. Self-contained (no ion-item
 * slots) so it drops into an <ion-item> the same way app-chat-row does.
 */
@Component({
  selector: 'app-reminder-row',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonNote],
  template: `
    <div class="row">
      <div class="main">
        <span class="msg">{{ reminder().title || reminder().message }}</span>
        @if (reminder().title) {
          <span class="sub">{{ reminder().message }}</span>
        }
        <ion-note class="when">{{ due() }} · {{ clock() }}</ion-note>
      </div>
    </div>
  `,
  styles: `
    :host { display: block; width: 100%; }
    .row { display: flex; align-items: center; width: 100%; padding: 0.1875rem 0; }
    .main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 0.125rem; }
    .msg {
      font-size: 0.9375rem; font-weight: 500;
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
    .sub {
      font-size: 0.8125rem; color: var(--ion-color-medium);
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
    .when { font-size: 0.75rem; }
  `,
})
export class ReminderRowComponent {
  readonly reminder = input.required<ReminderInfo>();

  protected readonly due = computed(() => dueIn(this.reminder().fireAt));
  protected readonly clock = computed(() => clockTime(this.reminder().fireAt));
}
