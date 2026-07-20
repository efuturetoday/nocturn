import {
  Component, ChangeDetectionStrategy, inject, input, effect, signal, untracked, viewChild,
} from '@angular/core';
import {
  IonHeader, IonToolbar, IonTitle, IonContent, IonButtons, IonBackButton, IonButton, IonIcon,
  IonFooter, IonTextarea, IonCard, IonCardHeader, IonCardTitle, IonCardContent, IonFab, IonFabButton,
  type ViewWillEnter, type ViewDidEnter, type ViewDidLeave,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { sendOutline, stopOutline, refreshOutline, chevronDownOutline } from 'ionicons/icons';
import { ChatService } from '../../core/services/chat.service';
import { ConnectionService } from '../../core/services/connection.service';
import { KeyboardService } from '../../core/services/keyboard.service';
import { MessageBubbleComponent } from './components/message-bubble';

@Component({
  selector: 'app-chat',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    IonHeader, IonToolbar, IonTitle, IonContent, IonButtons, IonBackButton, IonButton, IonIcon,
    IonFooter, IonTextarea, IonCard, IonCardHeader, IonCardTitle, IonCardContent, IonFab, IonFabButton,
    MessageBubbleComponent,
  ],
  template: `
    <ion-header>
      <ion-toolbar>
        <ion-buttons slot="start"><ion-back-button defaultHref="/tabs/chat" /></ion-buttons>
        <ion-title>Chat</ion-title>
        <ion-buttons slot="end">
          <ion-button (click)="reset()" title="New session">
            <ion-icon slot="icon-only" name="refresh-outline" />
          </ion-button>
        </ion-buttons>
      </ion-toolbar>
    </ion-header>

    <ion-content
      #content
      class="chat-content"
      [style.--padding-bottom.px]="kb.height()"
      [scrollEvents]="true"
      (ionScroll)="onScroll()"
    >
      @for (m of chat.messages(); track $index) {
        <app-message-bubble [message]="m" />
      }

      <!-- Always in the DOM; only toggle visibility (opacity/pointer-events). Mounting it with @if
           mid-scroll — exactly when atBottom flips crossing the threshold — inserts a slotted fixed
           element into ion-content and reflows, which stutters the scroll right as it appears. -->
      <ion-fab class="scroll-fab" [class.hidden]="atBottom()" slot="fixed" vertical="bottom" horizontal="end">
        <ion-fab-button size="small" aria-label="Scroll to latest" (click)="jumpToBottom()">
          <ion-icon name="chevron-down-outline" />
        </ion-fab-button>
      </ion-fab>
    </ion-content>

    @if (chat.pendingApproval(); as appr) {
      <ion-card color="warning">
        <ion-card-header><ion-card-title>Approval needed</ion-card-title></ion-card-header>
        <ion-card-content>
          <p>{{ appr.intent }}</p>
          <div class="approval-actions">
            @for (opt of appr.options; track $index) {
              <ion-button size="small" fill="solid" (click)="chat.resolve($index)">{{ opt }}</ion-button>
            }
          </div>
        </ion-card-content>
      </ion-card>
    }

    <ion-footer
      class="kb-follow"
      [style.transform]="'translateY(-' + kb.height() + 'px)'"
      [style.--kb-fill.px]="kb.height()"
    >
      <ion-toolbar class="composer">
        <ion-textarea
          class="composer-input"
          fill="outline"
          [autoGrow]="true"
          [rows]="1"
          placeholder="Message…"
          [value]="draft()"
          (ionInput)="draft.set($any($event.target).value ?? '')"
          [disabled]="!connection.connected()"
        />
        <ion-buttons slot="end">
          @if (chat.running()) {
            <ion-button color="danger" (click)="chat.cancel()">
              <ion-icon slot="icon-only" name="stop-outline" />
            </ion-button>
          } @else {
            <ion-button [disabled]="!draft().trim() || !connection.connected()" (click)="send()">
              <ion-icon slot="icon-only" name="send-outline" />
            </ion-button>
          }
        </ion-buttons>
      </ion-toolbar>
    </ion-footer>
  `,
  styles: `
    .chat-content { --padding-start: 16px; --padding-end: 16px; --padding-top: 12px; --padding-bottom: 12px; }
    /* Footer follows the keyboard: transform is set at keyboardWillShow (start of the iOS
       animation) and this transition matches the ~0.25s iOS curve → slides in sync, no late snap. */
    .kb-follow { position: relative; transition: transform 0.25s ease-out; will-change: transform; }
    /* Fill the strip below the lifted footer (behind the keyboard + its rounded top corners) with
       the toolbar colour, so chat content doesn't leak through. Height = keyboard height. */
    .kb-follow::after {
      content: '';
      position: absolute;
      top: 100%;
      left: 0;
      right: 0;
      height: var(--kb-fill, 0);
      background: var(--ion-toolbar-background, var(--ion-background-color-step-100));
    }
    .approval-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 8px; }
    .composer { --padding-start: 10px; --padding-end: 6px; --padding-top: 6px; --padding-bottom: 6px; }
    .composer-input {
      --background: var(--ion-background-color-step-100);
      --border-radius: 20px;
      --padding-start: 14px;
      --padding-end: 14px;
      margin: 0;
    }
    /* Jump-to-latest FAB: always mounted, shown/hidden via a class so it never mounts mid-scroll
       (which reflows and stutters the scroll). */
    .scroll-fab { transition: opacity 0.15s ease, transform 0.15s ease; }
    .scroll-fab.hidden { opacity: 0; pointer-events: none; transform: translateY(0.5rem); }
    /* Themed via ion-fab-button's own CSS variables (never reach into its shadow DOM) — a dark
       lifted surface with a hairline, primary-tinted chevron. */
    .scroll-fab ion-fab-button {
      --background: var(--ion-background-color-step-150);
      --background-activated: var(--ion-background-color-step-250);
      --background-hover: var(--ion-background-color-step-200);
      --color: var(--ion-color-primary);
      --border-color: var(--ion-background-color-step-200);
      --border-style: solid;
      --border-width: 1px;
      --box-shadow: 0 0.25rem 0.75rem rgba(0, 0, 0, 0.4);
    }
  `,
})
export class ChatPage implements ViewWillEnter, ViewDidEnter, ViewDidLeave {
  /** Bound from the `:id` route param via withComponentInputBinding(). */
  readonly id = input.required<string>();

  protected readonly chat = inject(ChatService);
  protected readonly connection = inject(ConnectionService);
  protected readonly kb = inject(KeyboardService);
  protected readonly draft = signal('');

  private readonly content = viewChild.required<IonContent>('content');
  // Whether the scroll is parked at (or near) the newest message. Drives auto-scroll: we only
  // follow the stream while the user is already at the bottom. If they scrolled up to read, a live
  // update must NOT yank them down — the jump-to-latest button appears instead.
  protected readonly atBottom = signal(true);

  constructor() {
    addIcons({ sendOutline, stopOutline, refreshOutline, chevronDownOutline });

    // Open the chat when the route param resolves/changes (ws = the active workspace). Viewing
    // state (which drives read-marking) is tied to the Ionic page lifecycle below, NOT here —
    // an Ionic tab shell keeps a routed page ALIVE when you switch away, so Angular's onDestroy
    // never fires on tab-away; ionViewDidLeave does. Marking read only while genuinely on screen
    // is what makes a background turnEnd raise the unread dot.
    effect(() => {
      const i = this.id();
      if (i) this.chat.openChat(i);
    });

    // Pin to the newest content while the user is at the bottom — instantly, no animation (both on
    // load and as the stream grows, like a messaging app). `atBottom` is read untracked so this
    // reacts to new messages, not to the user's own scrolling flipping the flag.
    effect(() => {
      this.chat.messages();
      if (untracked(this.atBottom)) void this.content().scrollToBottom(0);
    });
  }

  // Read-marking is bound to actual on-screen presence, not component lifetime: ionViewWillEnter
  // fires on first open AND on every return (tab-back / stack pop), ionViewDidLeave on every
  // leave — including a tab switch that keeps this page cached. Anything that finishes while we
  // are NOT viewing then stays unread (raises the dot in the list).
  ionViewWillEnter(): void {
    this.chat.startViewing(this.id());
    this.atBottom.set(true); // a freshly opened / returned chat starts pinned to the newest message
  }

  // After the page-transition animation completes and layout is final, land on the newest message
  // with no animation. ionViewDidEnter (not an effect) is the right hook — it fires once the view
  // is actually in place, so the jump can't race an unpainted DOM. Covers a cached chat that
  // produced no new message emission on re-enter.
  ionViewDidEnter(): void {
    void this.content().scrollToBottom(0);
  }

  ionViewDidLeave(): void {
    this.chat.stopViewing(this.id());
  }

  /** Track how close the scroll is to the bottom, so live updates only follow when already there. */
  protected async onScroll(): Promise<void> {
    const el = await this.content().getScrollElement();
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    this.atBottom.set(distanceFromBottom < 80);
  }

  /** Explicit user action (jump-to-latest button): smooth-scroll down and re-arm following. */
  protected jumpToBottom(): void {
    this.atBottom.set(true);
    void this.content().scrollToBottom(300);
  }

  protected send(): void {
    const text = this.draft().trim();
    if (!text) return;
    this.chat.submit(text);
    this.draft.set('');
  }

  protected reset(): void {
    this.chat.reset();
  }
}
