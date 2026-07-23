import { Component, ChangeDetectionStrategy, inject, computed } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonContent, IonList, IonItem, IonLabel, IonListHeader, IonIcon,
  IonItemSliding, IonItemOptions, IonItemOption,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { alarmOutline } from 'ionicons/icons';
import { ChatService } from '../../core/services/chat.service';
import { ChatListService } from '../../core/services/chat-list.service';
import { ReminderService } from '../../core/services/reminder.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import { ChatRowComponent } from '../chat/components/chat-row';
import { ReminderRowComponent } from './components/reminder-row';
import type { ChatMeta } from '../../core/protocol/nocturn-protocol';

@Component({
  selector: 'app-home',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    WorkspaceHeaderComponent, ChatRowComponent, ReminderRowComponent, IonContent, IonList,
    IonItem, IonLabel, IonListHeader, IonIcon, IonItemSliding, IonItemOptions, IonItemOption,
  ],
  template: `
    <app-workspace-header />

    <ion-content>
      <!-- Reminders sit at the very top: they are the only thing here with a deadline. The section
           is hidden entirely when nothing is pending — an empty "Reminders" header would be noise on
           every launch. Swipe a row to cancel; there is no create (the model sets them). -->
      @if (reminders.count()) {
        <ion-list inset="true">
          <ion-list-header><ion-label>Reminders</ion-label></ion-list-header>
          @for (r of reminders.reminders(); track r.id) {
            <ion-item-sliding>
              <ion-item lines="full">
                <ion-icon slot="start" name="alarm-outline" color="primary" aria-hidden="true" />
                <app-reminder-row [reminder]="r" />
              </ion-item>
              <ion-item-options side="end">
                <ion-item-option color="danger" (click)="cancelReminder(r.id)">Cancel</ion-item-option>
              </ion-item-options>
            </ion-item-sliding>
          }
        </ion-list>
      }

      <ion-list inset="true">
        <ion-list-header><ion-label>Recent chats</ion-label></ion-list-header>
        @for (c of recentChats(); track c.id) {
          <ion-item button detail="false" (click)="openChat(c)">
            <app-chat-row
              [chat]="c"
              [unread]="chatList.unreadIds().has(c.id)"
              [approval]="chatList.approvalWaiting().has(c.id)"
            />
          </ion-item>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No chats yet.</ion-label></ion-item>
        }
      </ion-list>

      @if (agentRuns().length) {
        <ion-list inset="true">
          <ion-list-header><ion-label>Agent runs</ion-label></ion-list-header>
          @for (c of agentRuns(); track c.id) {
            <ion-item button detail="false" (click)="openChat(c)">
              <app-chat-row
                [chat]="c"
                [unread]="chatList.unreadIds().has(c.id)"
                [approval]="chatList.approvalWaiting().has(c.id)"
              />
            </ion-item>
          }
        </ion-list>
      }
    </ion-content>
  `,
})
export class HomePage {
  protected readonly chat = inject(ChatService);
  protected readonly chatList = inject(ChatListService);
  protected readonly reminders = inject(ReminderService);
  private readonly router = inject(Router);

  constructor() {
    addIcons({ alarmOutline });
  }

  /** Cancel is not optimistic: the daemon broadcasts the change and the list refreshes from it. */
  protected cancelReminder(id: string): void {
    this.reminders.cancel(id);
  }

  // Three most-recent of each kind: human chats and agent runs.
  protected readonly recentChats = computed(() => this.latest((c) => c.source !== 'agent'));
  protected readonly agentRuns = computed(() => this.latest((c) => c.source === 'agent'));

  private latest(keep: (c: ChatMeta) => boolean): ChatMeta[] {
    return [...this.chatList.chats()]
      .filter(keep)
      .sort((a, b) => b.updated.localeCompare(a.updated))
      .slice(0, 3);
  }

  protected openChat(c: ChatMeta): void {
    this.chat.openChat(c.id);
    void this.router.navigate(['/chat', c.id]);
  }
}
