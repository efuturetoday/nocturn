import { Component, ChangeDetectionStrategy, inject, effect, signal, computed } from '@angular/core';
import { Router } from '@angular/router';
import { IonContent, IonList, IonItem, IonLabel, IonIcon, IonTextarea, IonButton } from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { arrowUpOutline } from 'ionicons/icons';
import { ChatService } from '../../core/services/chat.service';
import { WorkspaceService } from '../../core/services/workspace.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import { ChatRowComponent } from '../chat/components/chat-row';
import type { ChatMeta } from '../../core/protocol/nocturn-protocol';

@Component({
  selector: 'app-chats',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    WorkspaceHeaderComponent, ChatRowComponent, IonContent, IonList, IonItem, IonLabel, IonIcon,
    IonTextarea, IonButton,
  ],
  template: `
    <app-workspace-header />

    <ion-content>
      <!-- Ask box: type a question, it starts a new chat with that first message. -->
      <div class="ask">
        <ion-item class="ask-item" lines="none" shape="round" fill="outline">
          <ion-textarea
            [autoGrow]="true"
            [rows]="1"
            placeholder="Frag Nocturn…"
            [value]="draft()"
            (ionInput)="draft.set($any($event.target).value ?? '')"
            (keydown.enter)="$event.preventDefault(); startChat()"
          />
          <ion-button slot="end" fill="clear" size="small" [disabled]="!draft().trim()" (click)="startChat()">
            <ion-icon slot="icon-only" name="arrow-up-outline" />
          </ion-button>
        </ion-item>
      </div>

      <ion-list inset="true">
        @for (c of sorted(); track c.id) {
          <ion-item button detail="false" (click)="openChat(c)">
            <app-chat-row
              [chat]="c"
              [unread]="chat.unreadIds().has(c.id)"
              [approval]="chat.approvalWaiting().has(c.id)"
            />
          </ion-item>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No chats yet — ask something above.</ion-label></ion-item>
        }
      </ion-list>
    </ion-content>
  `,
  styles: `
    .ask { padding: 0.75rem 0.75rem 0.25rem; }
    .ask-item {
      --background: var(--ion-background-color-step-100);
      --border-color: var(--ion-background-color-step-200);
      --border-width: 1px;
      --border-radius: 1.25rem;
      --padding-start: 0.875rem;
      --inner-padding-end: 0.25rem;
      --min-height: 0;
    }
    .ask-item.item-has-focus { --border-color: var(--ion-color-primary); }
    .ask-item ion-textarea { --padding-top: 0.75rem; --padding-bottom: 0.75rem; }
  `,
})
export class ChatsPage {
  protected readonly chat = inject(ChatService);
  protected readonly workspaces = inject(WorkspaceService);
  private readonly router = inject(Router);

  protected readonly draft = signal('');
  // User chats only — agent runs live under the Agents tab, not here.
  protected readonly sorted = computed(() =>
    [...this.chat.chats()].filter((c) => c.source !== 'agent').sort((a, b) => b.updated.localeCompare(a.updated)),
  );

  constructor() {
    addIcons({ arrowUpOutline });
    // Load the active workspace's chats whenever it resolves/changes.
    effect(() => {
      if (this.workspaces.active()) this.chat.listChats();
    });
  }

  /** Start a fresh chat from the ask box: the chat page submits the queued first message, and the
      daemon replies chat.opened with the new chat's id. */
  protected startChat(): void {
    const q = this.draft().trim();
    if (!q) return;
    this.draft.set('');
    const id = this.chat.newChat(); // client-minted id → navigate straight to it (one transition)
    this.chat.queueFirstMessage(q);
    void this.router.navigate(['/chat', id]);
  }

  protected openChat(c: ChatMeta): void {
    this.blur();
    this.chat.openChat(c.id);
    void this.router.navigate(['/chat', c.id]);
  }

  private blur(): void {
    (document.activeElement as HTMLElement | null)?.blur();
  }
}
