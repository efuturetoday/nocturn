import { Component, ChangeDetectionStrategy, OnDestroy, inject, signal } from '@angular/core';
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

/** How long an allow must be held before it commits. A grant is the irreversible half of this
 * decision, so it costs deliberate contact; a refusal does not. Long enough that no stray touch
 * reaches it, short enough that answering an expected ask does not feel like a punishment. */
const HOLD_MS = 1500;

/** How often the countdown redraws while a button is held. */
const TICK_MS = 50;

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
 *
 * Every answer names its own verb rather than sitting under a shared heading, and the scope is NOT a
 * selector shared with Deny: a refusal is never remembered (gate.Check returns on `!approved`, before
 * it can store anything), so a duration offered next to "deny" would promise something the gate does
 * not keep.
 *
 * **Every allow is held for HOLD_MS, Deny is a tap.** Holding fills the button and counts the
 * remaining seconds down in its own caption; letting go early commits nothing. Only the grant is
 * guarded, because only the grant is what an accidental touch cannot take back — delaying a refusal
 * would just slow down the answer the gate already falls back to.
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
                <div class="hold grow" (pointerdown)="start(a, o, $event)" (pointerup)="cancel()" (pointercancel)="cancel()" (pointerleave)="cancel()">
                  <span class="fill" [style.transform]="fill(a, o)" aria-hidden="true"></span>
                  <ion-button color="primary">{{ caption(a, o, 'Allow once') }}</ion-button>
                </div>
              }
            </div>

            @for (o of keep(a); track o.id) {
              <div class="hold block" (pointerdown)="start(a, o, $event)" (pointerup)="cancel()" (pointercancel)="cancel()" (pointerleave)="cancel()">
                <span class="fill" [style.transform]="fill(a, o)" aria-hidden="true"></span>
                <ion-button expand="block" fill="outline" color="medium">
                  {{ caption(a, o, o.recall === 'session' ? 'Allow for this session' : 'Allow always') }}
                </ion-button>
              </div>
            }

            @if (widenings(a).length) {
              <p class="rule">or widen the grant</p>
              @for (o of widenings(a); track o.id) {
                <div class="hold block" (pointerdown)="start(a, o, $event)" (pointerup)="cancel()" (pointercancel)="cancel()" (pointerleave)="cancel()">
                  <span class="fill warn" [style.transform]="fill(a, o)" aria-hidden="true"></span>
                  <ion-button expand="block" fill="outline" color="warning">
                    @if (holding(a, o)) {
                      {{ caption(a, o, '') }}
                    } @else {
                      Allow always ·&nbsp;<span class="pattern">{{ o.widen?.target }}</span>
                    }
                  </ion-button>
                </div>
              }
            }

            <p class="hint">Hold an allow to confirm it.</p>
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
    .answer > ion-button { flex: 1 1 0; margin: 0; --border-radius: 0.75rem; height: 2.9rem; }

    /* The hold wrapper owns the pointer events and the progress fill, so no styling has to reach
       into ion-button's shadow DOM. A long press must not select text or raise the OS callout. */
    .hold {
      position: relative; overflow: hidden;
      border-radius: 0.75rem;
      touch-action: none; user-select: none; -webkit-touch-callout: none;
    }
    .hold.grow { flex: 1 1 0; }
    .hold.block { margin-top: 0.625rem; }
    .hold ion-button { margin: 0; --border-radius: 0.75rem; height: 2.9rem; width: 100%; pointer-events: none; }
    .fill {
      position: absolute; inset: 0; z-index: 1;
      transform-origin: left; transform: scaleX(0);
      background: var(--ion-text-color); opacity: 0.18;
      pointer-events: none;
    }
    .fill.warn { background: var(--ion-color-warning); opacity: 0.26; }

    /* The rule that separates reach from duration: everything above it grants the exact target. */
    .rule {
      display: flex; align-items: center; gap: 0.625rem;
      margin: 1.5rem 0 0.625rem;
      font-size: 0.72rem; letter-spacing: 0.05em; color: var(--ion-color-medium);
    }
    .rule::before, .rule::after {
      content: ''; flex: 1 1 0; height: 1px; background: var(--ion-border-color);
    }
    .rule + .hold.block { margin-top: 0; }

    .pattern { font-family: var(--ion-font-family-monospace, monospace); }

    .hint {
      margin: 0.875rem 0 0;
      font-size: 0.72rem; color: var(--ion-color-medium); text-align: center;
    }
  `,
})
export class ApprovalOverlayComponent implements OnDestroy {
  protected readonly approval = inject(ApprovalService);
  private readonly chatList = inject(ChatListService);

  protected readonly deny = DENY_OPTION;

  /** "<approvalId>:<optionId>" of the answer being held, or null. Only one can be held at a time. */
  private readonly held = signal<string | null>(null);
  /** Milliseconds left on that hold, ticked down for the caption and the fill. */
  private readonly left = signal(HOLD_MS);
  private timer?: ReturnType<typeof setInterval>;

  ngOnDestroy(): void {
    this.cancel();
  }

  /** Begin holding an allow. The deadline is a timestamp rather than a countdown of ticks, so a
   * throttled or coalesced interval still commits after HOLD_MS of real contact, never sooner. */
  protected start(a: PendingApproval, o: ApprovalOption, ev: PointerEvent): void {
    ev.preventDefault();
    this.cancel();
    const until = Date.now() + HOLD_MS;
    this.held.set(key(a, o));
    this.left.set(HOLD_MS);
    this.timer = setInterval(() => {
      const rest = until - Date.now();
      if (rest > 0) {
        this.left.set(rest);
        return;
      }
      this.cancel();
      this.approval.resolve(a.id, o.id);
    }, TICK_MS);
  }

  /** Let go: nothing commits, which is why a partial hold can never approve. Lifting early and the
   * OS stealing the gesture both land here. Dragging off cancels for a mouse only — a touch pointer
   * is implicitly captured by the element it went down on, so pointerleave never fires for it and
   * sliding a finger away still counts as contact until it lifts. */
  protected cancel(): void {
    clearInterval(this.timer);
    this.timer = undefined;
    this.held.set(null);
    this.left.set(HOLD_MS);
  }

  protected holding(a: PendingApproval, o: ApprovalOption): boolean {
    return this.held() === key(a, o);
  }

  /** The progress fill for one answer: empty unless it is the one being held. */
  protected fill(a: PendingApproval, o: ApprovalOption): string {
    if (!this.holding(a, o)) return 'scaleX(0)';
    return `scaleX(${1 - this.left() / HOLD_MS})`;
  }

  /** A held button counts down in place of its label, so the seconds are where the thumb already is. */
  protected caption(a: PendingApproval, o: ApprovalOption, label: string): string {
    if (!this.holding(a, o)) return label;
    return `hold ${(this.left() / 1000).toFixed(1)} s`;
  }

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

/** Identifies one answer across every open approval — several may offer the same option id. */
function key(a: PendingApproval, o: ApprovalOption): string {
  return `${a.id}:${o.id}`;
}
