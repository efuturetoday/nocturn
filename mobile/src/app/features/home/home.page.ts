import { Component, ChangeDetectionStrategy, inject, computed } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonContent, IonList, IonItem, IonLabel, IonIcon, IonListHeader,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { alarmOutline } from 'ionicons/icons';
import { ChatService } from '../../core/services/chat.service';
import { ReminderService } from '../../core/services/reminder.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import { ChatRowComponent } from '../chat/components/chat-row';
import type { ChatMeta } from '../../core/protocol/nocturn-protocol';

@Component({
  selector: 'app-home',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    WorkspaceHeaderComponent, ChatRowComponent, IonContent, IonList,
    IonItem, IonLabel, IonIcon, IonListHeader,
  ],
  template: `
    <app-workspace-header />

    <ion-content>
      @if (upcomingReminders().length) {
        <ion-list inset="true">
          <ion-list-header><ion-label>Reminders</ion-label></ion-list-header>
          @for (r of upcomingReminders(); track r.id) {
            <ion-item>
              <ion-icon slot="start" name="alarm-outline" color="medium" aria-hidden="true" />
              <ion-label>
                <h2>{{ r.title || r.message }}</h2>
                <p>{{ fireLabel(r.fireAt) }}</p>
              </ion-label>
            </ion-item>
          }
        </ion-list>
      }

      <ion-list inset="true">
        <ion-list-header><ion-label>Recent chats</ion-label></ion-list-header>
        @for (c of recentChats(); track c.id) {
          <ion-item button detail="false" (click)="openChat(c)">
            <app-chat-row
              [chat]="c"
              [unread]="chat.unreadIds().has(c.id)"
              [approval]="chat.approvalWaiting().has(c.id)"
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
                [unread]="chat.unreadIds().has(c.id)"
                [approval]="chat.approvalWaiting().has(c.id)"
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
  private readonly reminders = inject(ReminderService);
  private readonly router = inject(Router);

  // Three most-recent of each kind: human chats, agent runs, and next-to-fire reminders.
  protected readonly recentChats = computed(() => this.latest((c) => !c.agent));
  protected readonly agentRuns = computed(() => this.latest((c) => !!c.agent));
  protected readonly upcomingReminders = computed(() =>
    [...this.reminders.reminders()].sort((a, b) => a.fireAt.localeCompare(b.fireAt)).slice(0, 3),
  );

  private latest(keep: (c: ChatMeta) => boolean): ChatMeta[] {
    return [...this.chat.chats()]
      .filter(keep)
      .sort((a, b) => b.updated.localeCompare(a.updated))
      .slice(0, 3);
  }

  constructor() {
    addIcons({ alarmOutline });
  }

  /** Relative label for a future fire time — reminders count down, unlike chat rows. */
  protected fireLabel(iso: string): string {
    const at = new Date(iso).getTime();
    if (isNaN(at)) return '';
    const s = Math.round((at - Date.now()) / 1000);
    if (s <= 0) return 'due now';
    if (s < 60) return `in ${s}s`;
    const m = Math.round(s / 60);
    if (m < 60) return `in ${m}m`;
    const h = Math.round(m / 60);
    if (h < 24) return `in ${h}h`;
    return `in ${Math.round(h / 24)}d`;
  }

  protected openChat(c: ChatMeta): void {
    this.chat.openChat(c.id);
    void this.router.navigate(['/tabs', 'chat', c.id]);
  }
}
