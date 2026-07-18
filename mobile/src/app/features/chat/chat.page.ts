import {
  Component, ChangeDetectionStrategy, inject, input, effect, signal, viewChild, ElementRef,
} from '@angular/core';
import {
  IonHeader, IonToolbar, IonTitle, IonContent, IonButtons, IonBackButton, IonButton, IonIcon,
  IonFooter, IonTextarea, IonCard, IonCardHeader, IonCardTitle, IonCardContent,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { sendOutline, stopOutline, refreshOutline } from 'ionicons/icons';
import { ChatService } from '../../core/services/chat.service';
import { ConnectionService } from '../../core/services/connection.service';
import { KeyboardService } from '../../core/services/keyboard.service';
import { MessageBubbleComponent } from './components/message-bubble';

@Component({
  selector: 'app-chat',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    IonHeader, IonToolbar, IonTitle, IonContent, IonButtons, IonBackButton, IonButton, IonIcon,
    IonFooter, IonTextarea, IonCard, IonCardHeader, IonCardTitle, IonCardContent,
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

    <ion-content #content class="chat-content" [style.--padding-bottom.px]="kb.height()">
      @for (m of chat.messages(); track $index) {
        <app-message-bubble [message]="m" />
      }
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
      background: var(--ion-toolbar-background, var(--ion-color-step-100));
    }
    .approval-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 8px; }
    .composer { --padding-start: 10px; --padding-end: 6px; --padding-top: 6px; --padding-bottom: 6px; }
    .composer-input {
      --background: var(--ion-color-step-100);
      --border-radius: 20px;
      --padding-start: 14px;
      --padding-end: 14px;
      margin: 0;
    }
  `,
})
export class ChatPage {
  /** Bound from the `:id` route param via withComponentInputBinding(). */
  readonly id = input.required<string>();

  protected readonly chat = inject(ChatService);
  protected readonly connection = inject(ConnectionService);
  protected readonly kb = inject(KeyboardService);
  protected readonly draft = signal('');

  private readonly content = viewChild.required<IonContent>('content');

  constructor() {
    addIcons({ sendOutline, stopOutline, refreshOutline });

    // Open the chat when the route param resolves/changes (ws = the active workspace).
    effect(() => {
      const i = this.id();
      if (i) this.chat.openChat(i);
    });

    // Auto-scroll to the newest content as the stream grows.
    effect(() => {
      this.chat.messages();
      void this.content().scrollToBottom(200);
    });
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
