import { Component, ChangeDetectionStrategy, inject, effect, signal, computed } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonContent, IonList, IonItem, IonLabel, IonIcon, IonTextarea, IonButton,
  IonItemSliding, IonItemOptions, IonItemOption, AlertController,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { arrowUpOutline, createOutline, trashOutline } from 'ionicons/icons';
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
    IonTextarea, IonButton, IonItemSliding, IonItemOptions, IonItemOption,
  ],
  template: `
    <app-workspace-header />

    <ion-content>
      <!-- Gemini-style ask box: type a question, it starts a new chat with that first message. -->
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
          <ion-item-sliding>
            <ion-item button detail="false" (click)="openChat(c)">
              <app-chat-row
                [chat]="c"
                [unread]="chat.unreadIds().has(c.id)"
                [approval]="chat.approvalWaiting().has(c.id)"
              />
            </ion-item>
            <ion-item-options side="end">
              <ion-item-option (click)="rename(c)">
                <ion-icon slot="icon-only" name="create-outline" />
              </ion-item-option>
              <ion-item-option color="danger" (click)="remove(c)">
                <ion-icon slot="icon-only" name="trash-outline" />
              </ion-item-option>
            </ion-item-options>
          </ion-item-sliding>
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
  private readonly alerts = inject(AlertController);

  protected readonly draft = signal('');
  private readonly knownIds = signal<Set<string> | null>(null); // set while awaiting a new chat
  // User chats only — agent runs live under the Agents tab, not here.
  protected readonly sorted = computed(() =>
    [...this.chat.chats()].filter((c) => !c.agent).sort((a, b) => b.updated.localeCompare(a.updated)),
  );

  constructor() {
    addIcons({ arrowUpOutline, createOutline, trashOutline });
    // Load the active workspace's chats whenever it resolves/changes.
    effect(() => {
      if (this.workspaces.active()) this.chat.listChats();
    });
    // A fresh USER chat appeared → open it (its queued first message auto-sends on snapshot).
    effect(() => {
      const before = this.knownIds();
      if (!before) return;
      const fresh = this.chat.chats().find((c) => !before.has(c.id) && !c.agent);
      if (fresh) {
        this.knownIds.set(null);
        this.openChat(fresh);
      }
    });
  }

  protected startChat(): void {
    const q = this.draft().trim();
    if (!q) return;
    this.knownIds.set(new Set(this.chat.chats().map((c) => c.id)));
    this.chat.queueFirstMessage(q);
    this.chat.newChat(this.title(q));
    this.draft.set('');
    // Relies on the server's `chats` echo (after newChat) INCLUDING the new chat → the
    // "fresh chat" effect opens it. No polling. (Server must include the just-created chat
    // in that echo, or return its id — see note below.)
  }

  protected openChat(c: ChatMeta): void {
    this.blur();
    this.chat.openChat(c.id);
    void this.router.navigate(['/tabs', 'chat', c.id]);
  }

  protected async rename(c: ChatMeta): Promise<void> {
    const alert = await this.alerts.create({
      header: 'Rename chat',
      inputs: [{ name: 'name', type: 'text', value: c.name, placeholder: 'Name' }],
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        { text: 'Save', handler: (v) => this.chat.renameChat(c.id, (v.name ?? '').trim()) },
      ],
    });
    await alert.present();
  }

  protected async remove(c: ChatMeta): Promise<void> {
    const alert = await this.alerts.create({
      header: 'Delete chat?',
      message: c.name || 'Untitled chat',
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        { text: 'Delete', role: 'destructive', handler: () => this.chat.deleteChat(c.id) },
      ],
    });
    await alert.present();
  }

  /** A short chat title derived from the first question. */
  private title(q: string): string {
    const line = q.split('\n')[0].trim();
    return line.length > 40 ? line.slice(0, 40) + '…' : line;
  }

  private blur(): void {
    (document.activeElement as HTMLElement | null)?.blur();
  }
}
