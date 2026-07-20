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
      <div class="lead" aria-hidden="true">
        @if (unread() && !approval()) {
          <span class="dot" aria-label="unread"></span>
        }
      </div>
      <div class="main">
        <div class="text">
          <span class="name" [class.unread]="unread()">{{ chat().name || 'Untitled chat' }}</span>
          <div class="bottom">
            <span class="sub">{{ chat().turns }} {{ chat().turns === 1 ? 'message' : 'messages' }}</span>
            @if (approval()) {
              <ion-badge color="warning">approval</ion-badge>
            }
          </div>
        </div>
        <span class="time">{{ ago() }}</span>
      </div>
    </div>
  `,
  styles: `
    :host { display: block; width: 100%; }
    /* Apple-Mail layout: a fixed left gutter holds the unread dot (vertically centred). The gutter
       reserves its width even when read, so every name lines up on the same left edge. */
    .row { display: flex; align-items: center; gap: 0.5rem; width: 100%; padding: 0.1875rem 0; }
    .lead { flex-shrink: 0; width: 0.625rem; display: flex; justify-content: center; }
    .dot { width: 0.625rem; height: 0.625rem; border-radius: 50%; background: var(--ion-color-primary); }
    /* Text column on the left, timestamp vertically centred against the whole two-line block. */
    .main { flex: 1; min-width: 0; display: flex; align-items: center; gap: 0.5rem; }
    .text { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 0.125rem; }
    .name { font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .name.unread { font-weight: 700; }
    .bottom { display: flex; align-items: center; gap: 0.5rem; min-height: 1rem; }
    .sub { color: var(--ion-color-medium); font-size: 0.85rem; }
    .time { flex-shrink: 0; color: var(--ion-color-medium); font-size: 0.78rem; }
  `,
})
export class ChatRowComponent {
  readonly chat = input.required<ChatMeta>();
  readonly unread = input(false);
  readonly approval = input(false);
  protected readonly ago = computed(() => relativeTime(this.chat().updated));
}
