import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import { IonButton } from '@ionic/angular/standalone';
import { ApprovalService } from '../../core/services/approval.service';
import { ChatListService } from '../../core/services/chat-list.service';
import { DENY_OPTION, type ApprovalOption } from '../../core/protocol/nocturn-protocol';
import type { PendingApproval } from '../../core/services/chat-view';

/** The human label for a gate axis. The kinds are a closed set the daemon sends verbatim, so the
 * wording lives here where it cannot be reached by anything the model wrote. An unknown kind falls
 * back to the raw string — never to a dropped or guessed one. */
const KIND_LABELS: Record<string, string> = {
  net: 'Network access',
  file: 'File access',
  memory: 'Memory note',
  notify: 'Notification',
  remind: 'Reminder',
};

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
 * tool calls of one turn each hit the gate) stack in arrival order, separated by a rule.
 *
 * Nothing here is rendered from a sentence: the daemon sends the gate action's kind and target as
 * fields and the answers as {recall, widen} structure, and the labels below are ours. The two axes
 * an answer varies on are shown as two — `recall` is how LONG it is remembered, `widen` is how FAR
 * it reaches — so the broadest answer (permanent AND widened) cannot hide as another quiet chip
 * beside the exact one.
 */
@Component({
  selector: 'app-approval-overlay',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonButton],
  template: `
    @if (approval.pending().length; as n) {
      <div class="backdrop"></div>
      <div class="panel" role="dialog" aria-modal="true" aria-label="Approval needed">
        <div class="grabber" aria-hidden="true"></div>
        @if (n > 1) {
          <h2 class="title">{{ n }} approvals needed</h2>
        }

        @for (a of approval.pending(); track a.id) {
          <section class="ask">
            <div class="head">
              <span class="kicker">{{ kind(a) }}</span>
              @if (origin(a.chatId); as o) {
                <span class="badge" [class.agent]="o.source === 'agent'">{{ o.source === 'agent' ? 'Agent' : 'Chat' }}</span>
              }
            </div>

            @if (a.target) {
              <p class="target">{{ a.target }}</p>
            }
            @if (origin(a.chatId); as o) {
              <p class="from">{{ o.name }}</p>
            }

            <div class="answer">
              <ion-button fill="outline" color="danger" (click)="approval.resolve(a.id, deny)">Deny</ion-button>
              @if (once(a); as o) {
                <ion-button color="primary" (click)="approval.resolve(a.id, o.id)">Allow once</ion-button>
              }
            </div>

            @if (keep(a).length) {
              <p class="label">remember</p>
              <div class="keep">
                @for (o of keep(a); track o.id) {
                  <ion-button size="small" fill="outline" color="medium" (click)="approval.resolve(a.id, o.id)">
                    {{ o.recall === 'session' ? 'this session' : 'always' }}
                  </ion-button>
                }
              </div>
            }

            @if (widenings(a).length) {
              <p class="label wide">or widen the grant</p>
              @for (o of widenings(a); track o.id) {
                <ion-button
                  class="widen"
                  expand="block"
                  fill="outline"
                  color="warning"
                  (click)="approval.resolve(a.id, o.id)"
                >
                  always · <span class="pattern">{{ o.widen?.target }}</span>
                </ion-button>
              }
            }
          </section>
        }
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
      padding: 0.5rem 1.25rem calc(1.25rem + var(--ion-safe-area-bottom, 0px));
      background: var(--ion-background-color-step-50);
      border-top-left-radius: 1.25rem; border-top-right-radius: 1.25rem;
      box-shadow: 0 -0.5rem 1.5rem rgb(0 0 0 / 0.4);
      animation: slide-up 0.22s cubic-bezier(0.32, 0.72, 0, 1);
    }
    @keyframes fade { from { opacity: 0; } to { opacity: 1; } }
    @keyframes slide-up { from { transform: translateY(100%); } to { transform: translateY(0); } }

    /* Not a drag handle — the sheet is non-dismissable. It reads as a sheet, which is the point. */
    .grabber {
      width: 2.25rem; height: 0.25rem; margin: 0 auto 0.875rem;
      border-radius: 999px; background: var(--ion-background-color-step-250);
    }
    .title {
      margin: 0 0 1rem;
      font-size: 0.72rem; font-weight: 700; letter-spacing: 0.08em; text-transform: uppercase;
      color: var(--ion-color-medium);
    }

    /* Stacked approvals separate with a rule, not a card: the sheet already is the surface. */
    .ask + .ask { margin-top: 1.5rem; padding-top: 1.5rem; border-top: 1px solid var(--ion-border-color); }

    .head { display: flex; align-items: center; justify-content: space-between; gap: 0.75rem; }
    .kicker {
      font-size: 0.72rem; font-weight: 700; letter-spacing: 0.08em; text-transform: uppercase;
      color: var(--ion-color-medium);
    }
    .badge {
      flex-shrink: 0;
      font-size: 0.62rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.06em;
      padding: 0.15rem 0.45rem; border-radius: 999px;
      background: var(--ion-background-color-step-150); color: var(--ion-color-medium);
    }
    .badge.agent { background: var(--ion-color-tertiary); color: var(--ion-color-tertiary-contrast); }

    .target {
      margin: 0.35rem 0 0;
      font-family: var(--ion-font-family-monospace, monospace);
      font-size: 1.35rem; line-height: 1.2; word-break: break-word;
    }
    .from {
      margin: 0.35rem 0 0;
      color: var(--ion-color-medium); font-size: 0.82rem;
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }

    .answer { display: flex; gap: 0.625rem; margin-top: 1.25rem; }
    .answer ion-button { flex: 1 1 0; margin: 0; --border-radius: 0.75rem; height: 2.9rem; }

    .label {
      margin: 1.25rem 0 0.5rem;
      font-size: 0.72rem; letter-spacing: 0.05em; color: var(--ion-color-medium);
    }
    .label.wide {
      display: flex; align-items: center; gap: 0.625rem;
      margin-top: 1.5rem;
    }
    .label.wide::before, .label.wide::after {
      content: ''; flex: 1 1 0; height: 1px; background: var(--ion-border-color);
    }

    .keep { display: flex; flex-wrap: wrap; gap: 0.5rem; }
    .keep ion-button { flex: 1 1 auto; margin: 0; --border-radius: 999px; }

    .widen { margin: 0; --border-radius: 0.75rem; height: 2.75rem; }
    .widen .pattern { font-family: var(--ion-font-family-monospace, monospace); }
  `,
})
export class ApprovalOverlayComponent {
  protected readonly approval = inject(ApprovalService);
  private readonly chatList = inject(ChatListService);

  protected readonly deny = DENY_OPTION;

  /** The human label for this ask's axis; an unknown kind renders raw rather than disappearing. */
  protected kind(a: PendingApproval): string {
    return KIND_LABELS[a.kind] ?? a.kind;
  }

  /** The allow-once answer — the one that remembers nothing. */
  protected once(a: PendingApproval): ApprovalOption | undefined {
    return a.options.find((o) => o.recall === 'never' && !o.widen);
  }

  /** The answers that remember the EXACT target, longest-lived last. */
  protected keep(a: PendingApproval): ApprovalOption[] {
    return a.options.filter((o) => o.recall !== 'never' && !o.widen);
  }

  /** The suggested widenings — permanent AND broader, so they get their own zone. The daemon offers
   * at most one step today, but the list is plural because the wire is. */
  protected widenings(a: PendingApproval): ApprovalOption[] {
    return a.options.filter((o) => !!o.widen);
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
