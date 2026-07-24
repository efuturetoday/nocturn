import { Component, ChangeDetectionStrategy, input, output, signal } from '@angular/core';
import { IonToolbar, IonButtons, IonButton, IonIcon, IonTextarea } from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { sendOutline, stopOutline } from 'ionicons/icons';

/**
 * The message composer — the "submit part" shared by the chat detail and the chats list. An
 * autogrowing textarea + a send button that flips to a stop button while a turn is `running`. It owns
 * its own draft; the parent gets the trimmed text via `(send)` and (when running) `(cancel)`. It
 * renders only the `<ion-toolbar>`; the parent wraps it in `<ion-footer kbFollow>` for keyboard-follow.
 */
@Component({
  selector: 'app-composer',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonToolbar, IonButtons, IonButton, IonIcon, IonTextarea],
  template: `
    <ion-toolbar class="composer">
      <ion-textarea
        class="composer-input"
        fill="outline"
        [autoGrow]="true"
        [rows]="1"
        [placeholder]="placeholder()"
        [value]="draft()"
        (ionInput)="draft.set($any($event.target).value ?? '')"
        (keydown.enter)="$event.preventDefault(); submit()"
        [disabled]="disabled()"
      />
      <ion-buttons slot="end">
        @if (running()) {
          <ion-button color="danger" (click)="cancel.emit()">
            <ion-icon slot="icon-only" name="stop-outline" />
          </ion-button>
        } @else {
          <ion-button [disabled]="!draft().trim() || disabled()" (click)="submit()">
            <ion-icon slot="icon-only" name="send-outline" />
          </ion-button>
        }
      </ion-buttons>
    </ion-toolbar>
  `,
  styles: `
    .composer { --padding-start: 10px; --padding-end: 6px; --padding-top: 6px; --padding-bottom: 6px; }
    .composer-input {
      --background: var(--ion-background-color-step-100);
      --border-radius: 20px;
      --padding-start: 14px;
      --padding-end: 14px;
      margin: 0;
    }
  `,
})
export class ComposerComponent {
  readonly placeholder = input('Message…');
  readonly disabled = input(false);
  readonly running = input(false);

  readonly send = output<string>();
  readonly cancel = output<void>();

  protected readonly draft = signal('');

  constructor() {
    addIcons({ sendOutline, stopOutline });
  }

  protected submit(): void {
    const text = this.draft().trim();
    if (!text) return;
    this.send.emit(text);
    this.draft.set('');
  }
}
