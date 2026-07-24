import {
  Component, ChangeDetectionStrategy, inject, input, effect, signal, untracked, viewChild, ElementRef,
} from '@angular/core';
import {
  IonHeader, IonToolbar, IonTitle, IonContent, IonButtons, IonBackButton, IonIcon,
  IonFooter, IonFab, IonFabButton,
  type ViewWillEnter, type ViewWillLeave, type ViewDidEnter, type ViewDidLeave,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { chevronDownOutline } from 'ionicons/icons';
import { ActivatedRoute } from '@angular/router';
import { ChatService } from '../../core/services/chat.service';
import { AgentRunService } from '../../core/services/agent-run.service';
import { ConversationService } from '../../core/services/conversation.service';
import { ChatListService } from '../../core/services/chat-list.service';
import type { Source } from '../../core/protocol/nocturn-protocol';
import { ConnectionService } from '../../core/services/connection.service';
import { KeyboardService } from '../../core/services/keyboard.service';
import { MessageBubbleComponent } from './components/message-bubble';
import { ComposerComponent } from '../../shared/composer';
import { KbFollowDirective } from '../../shared/kb-follow.directive';

@Component({
  selector: 'app-chat',
  changeDetection: ChangeDetectionStrategy.OnPush,
  // The reused page binds to whichever conversation store its route selects (data.kind): user chats
  // → ChatService, agent runs → AgentRunService. Provided as the ConversationService token so children
  // (e.g. tool-frame) resolve the SAME route-correct instance without knowing the kind.
  providers: [
    {
      provide: ConversationService,
      useFactory: (route: ActivatedRoute, user: ChatService, agent: AgentRunService) =>
        route.snapshot.data['kind'] === 'agent' ? agent : user,
      deps: [ActivatedRoute, ChatService, AgentRunService],
    },
  ],
  imports: [
    IonHeader, IonToolbar, IonTitle, IonContent, IonButtons, IonBackButton, IonIcon,
    IonFooter, IonFab, IonFabButton,
    MessageBubbleComponent, ComposerComponent, KbFollowDirective,
  ],
  template: `
    <ion-header>
      <ion-toolbar>
        <ion-buttons slot="start"><ion-back-button defaultHref="/tabs/chat" /></ion-buttons>
        <ion-title>Chat</ion-title>
      </ion-toolbar>
    </ion-header>

    <ion-content
      #content
      class="chat-content"
      [style.--padding-bottom.px]="kb.height()"
      [scrollEvents]="true"
      (ionScroll)="onScroll()"
    >
      @for (m of convo.messages(); track $index) {
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

    <ion-footer #footer kbFollow>
      <app-composer
        [running]="convo.running()"
        [disabled]="!connection.connected()"
        (send)="convo.submit($event)"
        (cancel)="convo.cancel()"
      />
    </ion-footer>
  `,
  styles: `
    .chat-content { --padding-start: 16px; --padding-end: 16px; --padding-top: 12px; --padding-bottom: 12px; }
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
export class ChatPage implements ViewWillEnter, ViewWillLeave, ViewDidEnter, ViewDidLeave {
  /** The chat id, bound from the `:id` route param via withComponentInputBinding(). Client-minted, so
      it is set for a fresh chat too (navigated to straight from the ask box). */
  readonly id = input<string>();
  /** The conversation kind, bound from the route's `data.kind` via withComponentInputBinding(). */
  readonly kind = input<Source>('user');

  /** The route-selected active conversation (user chats or agent runs) — the page binds to this. */
  protected readonly convo = inject(ConversationService);
  // The user-chat service specifically, only for the fresh-chat first-message queue (a user concept;
  // agent runs are server-created and never carry a queued first message).
  private readonly userChat = inject(ChatService);
  private readonly chatList = inject(ChatListService);
  protected readonly connection = inject(ConnectionService);
  protected readonly kb = inject(KeyboardService);

  private readonly content = viewChild.required<IonContent>('content');
  private readonly contentEl = viewChild.required('content', { read: ElementRef });
  private readonly footer = viewChild.required('footer', { read: ElementRef });
  private lastScrollTop = 0;
  // Whether the scroll is parked at (or near) the newest message. Drives auto-scroll: we only
  // follow the stream while the user is already at the bottom. If they scrolled up to read, a live
  // update must NOT yank them down — the jump-to-latest button appears instead.
  protected readonly atBottom = signal(true);

  constructor() {
    addIcons({ chevronDownOutline });

    // Open the chat when the route param resolves/changes (ws = the active workspace). Viewing
    // state (which drives read-marking) is tied to the Ionic page lifecycle below, NOT here —
    // an Ionic tab shell keeps a routed page ALIVE when you switch away, so Angular's onDestroy
    // never fires on tab-away; ionViewDidLeave does. Marking read only while genuinely on screen
    // is what makes a background turnEnd raise the unread dot.
    effect(() => {
      const i = this.id();
      if (!i) return;
      // A fresh chat (client-minted id) arrives here with its id already active (set by newChat) and a
      // queued first message — send it (an unknown id creates the chat on the daemon). Otherwise this
      // is an existing chat: open it for its snapshot, unless it's already the active one (don't
      // re-open — openChat resets local state and would wipe the live view).
      // A queued first message applies only to a freshly minted USER chat (agent runs are
      // server-created); otherwise open the conversation for its snapshot unless it is already active.
      const first = this.kind() === 'user' ? untracked(() => this.userChat.takePendingFirst()) : null;
      if (first) {
        this.convo.submit(first);
      } else if (i !== untracked(() => this.convo.activeChatId())) {
        this.convo.openChat(i);
      }
    });

    // Pin to the newest content while the user is at the bottom — instantly, no animation (both on
    // load and as the stream grows, like a messaging app). `atBottom` is read untracked so this
    // reacts to new messages, not to the user's own scrolling flipping the flag.
    effect(() => {
      this.convo.messages();
      if (!untracked(this.atBottom)) return;
      // Defer past this render: scrollToBottom reads scrollHeight, which is still stale if we call
      // it before the @for lays out the just-appended token — so the view lags one frame behind the
      // stream and never truly sticks to the bottom. rAF runs after layout, so we reach the real end.
      requestAnimationFrame(() => void this.content().scrollToBottom(0));
    });

    // The keyboard opening lifts the footer + grows the content's bottom padding, pushing the newest
    // message up out of view even though nothing new arrived. Re-pin to the bottom when the height
    // changes, but only if we were already following — so opening it while scrolled up doesn't yank.
    effect(() => {
      this.kb.height();
      if (!untracked(this.atBottom)) return;
      requestAnimationFrame(() => void this.content().scrollToBottom(0));
    });
  }

  // Read-marking is bound to actual on-screen presence, not component lifetime: ionViewWillEnter
  // fires on first open AND on every return (tab-back / stack pop), ionViewDidLeave on every
  // leave — including a tab switch that keeps this page cached. Anything that finishes while we
  // are NOT viewing then stays unread (raises the dot in the list).
  ionViewWillEnter(): void {
    const i = this.id();
    if (i) this.chatList.startViewing(i);
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
    const i = this.id();
    if (i) this.chatList.stopViewing(i);
  }

  // Collapse the keyboard-lift the INSTANT the leave starts — the leaving OnPush view isn't
  // change-detected during the transition, so if we navigate with the keyboard open the footer's
  // transform + the content's bottom padding stay stale (a big blank filler that only animates away
  // at the end). Remove the inline styles imperatively (CD-independent); the directive/binding re-apply
  // on re-enter. This RESETS (footer back to its normal spot), it does NOT hide it — so a CANCELED
  // swipe-back leaves a normal, visible composer.
  ionViewWillLeave(): void {
    const f = this.footer().nativeElement as HTMLElement;
    f.classList.remove('kb-follow'); // make it a vanilla footer for the slide (no compositor pin)
    f.style.removeProperty('transform');
    f.style.removeProperty('--kb-fill');
    (this.contentEl().nativeElement as HTMLElement).style.removeProperty('--padding-bottom');
  }

  /** Track how close the scroll is to the bottom, so live updates only follow when already there.
   * A swipe DOWN on the messages (scrollTop drops) dismisses an open keyboard, like iOS Messages. */
  protected async onScroll(): Promise<void> {
    const el = await this.content().getScrollElement();
    if (this.kb.open() && el.scrollTop < this.lastScrollTop - 4) this.kb.dismiss();
    this.lastScrollTop = el.scrollTop;
    this.atBottom.set(el.scrollHeight - el.scrollTop - el.clientHeight < 80);
  }

  /** Explicit user action (jump-to-latest button): smooth-scroll down and re-arm following. */
  protected jumpToBottom(): void {
    this.atBottom.set(true);
    void this.content().scrollToBottom(300);
  }
}
