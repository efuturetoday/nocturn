import { Component, ChangeDetectionStrategy, input, computed } from '@angular/core';
import { IonBadge } from '@ionic/angular/standalone';
import type { ChatMeta } from '../../../core/protocol/nocturn-protocol';

/** Relative time for an RFC3339 timestamp, Apple-Mail style. */
export function relativeTime(iso: string): string {
  const then = new Date(iso).getTime();
  if (isNaN(then)) return '';
  const s = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (s < 60) return 'now';
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  if (d === 1) return 'yesterday';
  if (d < 7) return `${d}d ago`;
  if (d < 14) return 'last week';
  return new Date(then).toLocaleDateString();
}

/**
 * One lean chat-list row (Apple-Mail style): name + relative time on top, message count below,
 * unread dot / approval badge on the right. Self-contained (no ion-item slots), so it drops into
 * an <ion-item> in the chat list AND the home dashboard — one source of truth for the row.
 */
@Component({
  selector: 'app-chat-row',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonBadge],
  template: `
    <div class="row">
      <div class="top">
        <span class="name" [class.unread]="unread()">{{ chat().name || 'Untitled chat' }}</span>
        <span class="time">{{ ago() }}</span>
      </div>
      <div class="bottom">
        <span class="sub">{{ chat().turns }} messages</span>
        @if (approval()) {
          <ion-badge color="warning">approval</ion-badge>
        } @else if (unread()) {
          <span class="dot" aria-label="unread"></span>
        }
      </div>
    </div>
  `,
  styles: `
    :host { display: block; width: 100%; }
    .row { display: flex; flex-direction: column; gap: 2px; width: 100%; padding: 3px 0; }
    .top { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
    .name { font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .name.unread { font-weight: 700; }
    .time { flex-shrink: 0; color: var(--ion-color-medium); font-size: 0.78rem; }
    .bottom { display: flex; justify-content: space-between; align-items: center; min-height: 16px; }
    .sub { color: var(--ion-color-medium); font-size: 0.85rem; }
    .dot { width: 10px; height: 10px; border-radius: 50%; background: var(--ion-color-primary); }
  `,
})
export class ChatRowComponent {
  readonly chat = input.required<ChatMeta>();
  readonly unread = input(false);
  readonly approval = input(false);
  protected readonly ago = computed(() => relativeTime(this.chat().updated));
}
