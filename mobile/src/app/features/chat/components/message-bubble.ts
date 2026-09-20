import { Component, ChangeDetectionStrategy, input, inject, computed, signal } from '@angular/core';
import {
  IonNote,
  IonSpinner,
  IonModal,
  IonHeader,
  IonToolbar,
  IonTitle,
  IonButtons,
  IonButton,
  IonContent,
} from '@ionic/angular/standalone';
import { LucideWrench, LucideChevronRight, LucideX } from '@lucide/angular';
import { ToolFrameComponent } from './tool-frame';
import { MarkdownComponent } from '../../../shared/markdown';
import { ApprovalService } from '../../../core/services/approval.service';
import type { ChatMessageView } from '../../../core/services/chat-view';

/** One conversation message: user bubble, or assistant turn with dim reasoning + tool forest. */
@Component({
  selector: 'app-message-bubble',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    IonNote,
    IonSpinner,
    IonModal,
    IonHeader,
    IonToolbar,
    IonTitle,
    IonButtons,
    IonButton,
    IonContent,
    ToolFrameComponent,
    MarkdownComponent,
    LucideWrench,
    LucideChevronRight,
    LucideX,
  ],
  host: {
    class: 'message-bubble',
    '[class.user]': 'isUser()',
    '[class.assistant]': '!isUser()',
  },
  template: `
    @if (message().thinking) {
      <ion-note class="thinking" color="medium">{{ message().thinking }}</ion-note>
    }

    <!-- One finger-sized trigger for the whole turn's tools — tapping opens a window with EVERY tool
         as an accordion, so a fat finger never has to pick one tiny row out of the forest. -->
    @if (message().tools.length) {
      <button
        type="button"
        class="tools-trigger"
        (click)="open.set(true)"
        [attr.aria-expanded]="open() ? 'true' : 'false'"
        aria-haspopup="dialog"
      >
        @if (anyRunning()) {
          <ion-spinner name="dots" />
        } @else {
          <svg lucideWrench [size]="16" />
        }
        <span class="summary">{{ toolSummary() }}</span>
        @if (anyWaiting()) {
          <span class="wait">needs approval</span>
        }
        <svg lucideChevronRight class="chev" [size]="16" />
      </button>

      <ion-modal [isOpen]="open()" (didDismiss)="open.set(false)">
        <ng-template>
          <ion-header>
            <ion-toolbar>
              <ion-title>Tools</ion-title>
              <ion-buttons slot="end">
                <ion-button (click)="open.set(false)" aria-label="Close">
                  <svg lucideX [size]="22" />
                </ion-button>
              </ion-buttons>
            </ion-toolbar>
          </ion-header>
          <ion-content class="tools-window">
            @for (t of message().tools; track t.key) {
              <app-tool-frame [tool]="t" [style.margin-left.px]="t.depth * 16" />
            }
          </ion-content>
        </ng-template>
      </ion-modal>
    }

    @if (message().content) {
      @if (isUser()) {
        <div class="plain">{{ message().content }}</div>
      } @else {
        <app-markdown [text]="message().content" />
      }
    }
    @if (message().error; as err) {
      <ion-note color="danger">{{ err }}</ion-note>
    }
    @if (message().pending && !message().content) {
      <ion-spinner name="dots" />
    }
  `,
  styles: `
    :host {
      display: block;
      width: fit-content;
      max-width: 85%;
      margin: 0.375rem 0;
      padding: 0.625rem 0.875rem;
      border-radius: 1rem;
      word-break: break-word;
    }
    .plain { white-space: pre-wrap; }
    .thinking { white-space: pre-wrap; }
    :host.user {
      margin-left: auto;
      background: var(--ion-color-primary);
      color: var(--ion-color-primary-contrast);
      border-bottom-right-radius: 0.25rem;
    }
    :host.assistant {
      margin-right: auto;
      background: var(--ion-background-color-step-100);
      color: var(--ion-text-color);
      border-bottom-left-radius: 0.25rem;
    }
    .thinking { display: block; font-style: italic; margin-bottom: 0.375rem; }

    /* Trigger row: full width, finger-sized — the single tap target for the turn's tools. */
    .tools-trigger {
      display: flex; align-items: center; gap: 0.5rem; width: 100%;
      margin-bottom: 0.375rem; padding: 0.375rem 0.5rem;
      background: var(--ion-background-color-step-150); border: 0; border-radius: 0.625rem;
      color: inherit; text-align: left; cursor: pointer; font-size: 0.8rem; min-height: 2.25rem;
    }
    .tools-trigger > svg { color: var(--ion-color-medium); }
    .tools-trigger ion-spinner { width: 1rem; height: 1rem; flex-shrink: 0; }
    .tools-trigger .summary {
      flex: 1; min-width: 0; font-family: var(--ion-font-family-monospace, monospace);
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
    .tools-trigger .wait { color: var(--ion-color-warning); flex-shrink: 0; font-size: 0.78rem; }
    .tools-trigger .chev { opacity: 0.5; }

    .tools-window { --padding-start: 0.75rem; --padding-end: 0.75rem; --padding-top: 0.5rem; --padding-bottom: 1.5rem; }
  `,
})
export class MessageBubbleComponent {
  readonly message = input.required<ChatMessageView>();
  private readonly approvals = inject(ApprovalService);
  protected readonly open = signal(false);

  protected readonly isUser = computed(() => this.message().role === 'user');
  protected readonly anyRunning = computed(() => this.message().tools.some((t) => t.running));

  // An open approval belongs to this turn if its frame matches any of the turn's tool calls.
  protected readonly anyWaiting = computed(() => {
    const frames = this.approvals.frames();
    return this.message().tools.some((t) => t.id != null && frames.has(t.id));
  });

  // A compact one-line list of the tool names — the trigger's label (e.g. `code_run · http_read +1`).
  protected readonly toolSummary = computed(() => {
    const names = this.message().tools.map((t) => t.tool);
    const head = names.slice(0, 3).join(' · ');
    return names.length > 3 ? `${head} +${names.length - 3}` : head;
  });

  constructor() {}
}
