import { Component, ChangeDetectionStrategy, input, computed } from '@angular/core';
import { IonNote, IonSpinner } from '@ionic/angular/standalone';
import { ToolFrameComponent } from './tool-frame';
import { MarkdownComponent } from '../../../shared/markdown';
import type { ChatMessageView } from '../../../core/services/chat.service';

/** One conversation message: user bubble, or assistant turn with dim reasoning + tool forest. */
@Component({
  selector: 'app-message-bubble',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonNote, IonSpinner, ToolFrameComponent, MarkdownComponent],
  host: {
    class: 'message-bubble',
    '[class.user]': 'isUser()',
    '[class.assistant]': '!isUser()',
  },
  template: `
    @if (message().thinking) {
      <ion-note class="thinking" color="medium">{{ message().thinking }}</ion-note>
    }
    @if (message().tools.length) {
      <div class="tools">
        @for (t of message().tools; track t.key) {
          <app-tool-frame [tool]="t" [style.margin-left.px]="t.depth * 16" />
        }
      </div>
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
      max-width: 85%;
      margin: 6px 0;
      padding: 10px 14px;
      border-radius: 16px;
      word-break: break-word;
    }
    .plain { white-space: pre-wrap; }
    .thinking { white-space: pre-wrap; }
    :host.user {
      margin-left: auto;
      background: var(--ion-color-primary);
      color: var(--ion-color-primary-contrast);
      border-bottom-right-radius: 4px;
    }
    :host.assistant {
      margin-right: auto;
      background: var(--ion-color-step-100, #f2f2f2);
      color: var(--ion-text-color);
      border-bottom-left-radius: 4px;
    }
    .thinking { display: block; font-style: italic; margin-bottom: 6px; }
    .tools { margin-bottom: 6px; }
  `,
})
export class MessageBubbleComponent {
  readonly message = input.required<ChatMessageView>();
  protected readonly isUser = computed(() => this.message().role === 'user');
}
