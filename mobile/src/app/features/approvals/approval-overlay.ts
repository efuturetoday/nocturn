import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import { IonButton, IonIcon } from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { warningOutline } from 'ionicons/icons';
import { ApprovalService } from '../../core/services/approval.service';
import { ChatListService } from '../../core/services/chat-list.service';

/**
 * The app-global out-of-band approval overlay. Mounted once in the app root (like the connection
 * pill), it floats over every route so a request raised anywhere — another chat, another tab, a
 * background agent run — is answerable in context.
 *
 * Visibility is a PURE function of the signal: `@if (approval.pending().length)`. There is no
 * open/close lifecycle to desync from the state (the bug the old ModalController presenter had — it
 * could leave an empty sheet open after the last approval resolved). It is non-dismissable by
 * construction: a plain element with no swipe gesture and a no-op backdrop, so it vanishes ONLY when
 * `pending()` empties — an approval is a mandatory decision. Several concurrent approvals (parallel
 * tool calls of one turn each hit the gate) stack as cards in arrival order.
 */
@Component({
  selector: 'app-approval-overlay',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonButton, IonIcon],
  template: `
    @if (approval.pending().length) {
      <div class="backdrop"></div>
      <div class="panel" role="dialog" aria-modal="true" aria-label="Approval needed">
        <h2 class="title">{{ title() }}</h2>

        <div class="cards">
          @for (a of approval.pending(); track a.id) {
            <section class="card">
              @if (origin(a.chatId); as o) {
                <div class="origin">
                  <span class="badge" [class.agent]="o.source === 'agent'">{{ o.source === 'agent' ? 'Agent' : 'Chat' }}</span>
                  <span class="name">{{ o.name }}</span>
                </div>
              }

              <div class="intent">
                <ion-icon name="warning-outline" aria-hidden="true" />
                <span class="effect">{{ a.intent }}</span>
              </div>

              <div class="actions">
                <div class="allow-row">
                  @for (opt of a.options; track $index) {
                    <ion-button size="small" color="primary" (click)="approval.resolve(a.id, $index)">{{ opt }}</ion-button>
                  }
                </div>
                <ion-button expand="block" fill="outline" color="medium" (click)="approval.resolve(a.id, -1)">Deny</ion-button>
              </div>
            </section>
          }
        </div>
      </div>
    }
  `,
  styles: `
    .backdrop {
      position: fixed; inset: 0; z-index: 1000;
      background: rgba(0, 0, 0, 0.55);
      animation: fade 0.15s ease-out;
    }
    .panel {
      position: fixed; left: 0; right: 0; bottom: 0; z-index: 1001;
      max-height: 85vh; overflow-y: auto;
      padding: 1rem 1rem calc(1.25rem + var(--ion-safe-area-bottom, 0px));
      background: var(--ion-background-color);
      border-top-left-radius: 1.25rem; border-top-right-radius: 1.25rem;
      box-shadow: 0 -0.5rem 1.5rem rgb(0 0 0 / 0.4);
      animation: slide-up 0.22s cubic-bezier(0.32, 0.72, 0, 1);
    }
    @keyframes fade { from { opacity: 0; } to { opacity: 1; } }
    @keyframes slide-up { from { transform: translateY(100%); } to { transform: translateY(0); } }

    .title { margin: 0 0 0.875rem; font-size: 1.05rem; font-weight: 700; }

    .card {
      background: var(--ion-background-color-step-100);
      border: 1px solid var(--ion-background-color-step-150);
      border-left: 3px solid var(--ion-color-warning);
      border-radius: 0.75rem;
      padding: 0.875rem 1rem;
      margin-bottom: 0.75rem;
    }
    .card:last-child { margin-bottom: 0; }

    .origin { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.625rem; min-width: 0; }
    .badge {
      flex-shrink: 0;
      font-size: 0.68rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.04em;
      padding: 0.15rem 0.5rem; border-radius: 999px;
      background: var(--ion-color-primary); color: var(--ion-color-primary-contrast);
    }
    .badge.agent { background: var(--ion-color-tertiary); color: var(--ion-color-tertiary-contrast); }
    .name {
      color: var(--ion-color-medium); font-size: 0.85rem;
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }

    .intent { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.875rem; }
    .intent ion-icon { color: var(--ion-color-warning); font-size: 1.15rem; flex-shrink: 0; }
    .effect {
      font-family: var(--ion-font-family-monospace, monospace);
      font-size: 0.95rem; line-height: 1.35; word-break: break-word;
    }

    .actions { display: flex; flex-direction: column; gap: 0.5rem; }
    .allow-row { display: flex; flex-wrap: wrap; gap: 0.5rem; }
    .allow-row ion-button { flex: 1 1 auto; margin: 0; --border-radius: 0.625rem; }
    .actions > ion-button { margin: 0; --border-radius: 0.625rem; }
  `,
})
export class ApprovalOverlayComponent {
  protected readonly approval = inject(ApprovalService);
  private readonly chatList = inject(ChatListService);

  protected title(): string {
    const n = this.approval.pending().length;
    return n > 1 ? `${n} approvals needed` : 'Approval needed';
  }

  constructor() {
    addIcons({ warningOutline });
  }

  /** Resolve a chat id to a display origin (name + kind) from the loaded chat list. Absent id → no
   * provenance; a known id not in the list → best-effort label (a background/other-workspace run). */
  protected origin(chatId?: string): { name: string; source: string } | null {
    if (!chatId) return null;
    const c = this.chatList.chats().find((x) => x.id === chatId);
    if (c) return { name: c.name || 'Untitled', source: c.source };
    return { name: 'Background run', source: 'agent' };
  }
}
