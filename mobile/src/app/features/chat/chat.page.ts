import {
  Component, ChangeDetectionStrategy, inject, input, effect, signal, viewChild, ElementRef,
} from '@angular/core';
import {
  IonHeader, IonToolbar, IonTitle, IonContent, IonButtons, IonBackButton, IonButton, IonIcon,
  IonFooter, IonTextarea, IonCard, IonCardHeader, IonCardTitle, IonCardContent, IonBadge,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { sendOutline, stopOutline, refreshOutline } from 'ionicons/icons';
import { ChatService } from '../../core/services/chat.service';
import { ConnectionService } from '../../core/services/connection.service';
import { MessageBubbleComponent } from './components/message-bubble';

@Component({
  selector: 'app-chat',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    IonHeader, IonToolbar, IonTitle, IonContent, IonButtons, IonBackButton, IonButton, IonIcon,
    IonFooter, IonTextarea, IonCard, IonCardHeader, IonCardTitle, IonCardContent, IonBadge,
    MessageBubbleComponent,
  ],
  template: `
    <ion-header>
      <ion-toolbar>
        <ion-buttons slot="start"><ion-back-button [defaultHref]="'/' + ws() + '/chats'" /></ion-buttons>
        <ion-title>Chat</ion-title>
        <ion-buttons slot="end">
          @if (connection.state() !== 'connected') {
            <ion-badge color="warning">{{ connection.state() }}</ion-badge>
          }
          <ion-button (click)="reset()" title="New session">
            <ion-icon slot="icon-only" name="refresh-outline" />
          </ion-button>
        </ion-buttons>
      </ion-toolbar>
    </ion-header>

    <ion-content #content class="ion-padding">
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

    <ion-footer>
      <ion-toolbar>
        <ion-textarea
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
    .approval-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 8px; }
  `,
})
export class ChatPage {
  /** Bound from `:ws` / `:id` route params via withComponentInputBinding(). */
  readonly ws = input.required<string>();
  readonly id = input.required<string>();

  protected readonly chat = inject(ChatService);
  protected readonly connection = inject(ConnectionService);
  protected readonly draft = signal('');

  private readonly content = viewChild.required<IonContent>('content');

  constructor() {
    addIcons({ sendOutline, stopOutline, refreshOutline });

    // Open the chat when the route params resolve/change.
    effect(() => {
      const w = this.ws();
      const i = this.id();
      if (w && i) this.chat.openChat(w, i);
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
