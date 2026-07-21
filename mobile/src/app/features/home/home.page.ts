import { Component, ChangeDetectionStrategy, inject, computed } from '@angular/core';
import { Router } from '@angular/router';
import { IonContent, IonList, IonItem, IonLabel, IonListHeader } from '@ionic/angular/standalone';
import { ChatService } from '../../core/services/chat.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import { ChatRowComponent } from '../chat/components/chat-row';
import type { ChatMeta } from '../../core/protocol/nocturn-protocol';

@Component({
  selector: 'app-home',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    WorkspaceHeaderComponent, ChatRowComponent, IonContent, IonList,
    IonItem, IonLabel, IonListHeader,
  ],
  template: `
    <app-workspace-header />

    <ion-content>
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
  private readonly router = inject(Router);

  // Three most-recent of each kind: human chats and agent runs.
  protected readonly recentChats = computed(() => this.latest((c) => c.source !== 'agent'));
  protected readonly agentRuns = computed(() => this.latest((c) => c.source === 'agent'));

  private latest(keep: (c: ChatMeta) => boolean): ChatMeta[] {
    return [...this.chat.chats()]
      .filter(keep)
      .sort((a, b) => b.updated.localeCompare(a.updated))
      .slice(0, 3);
  }

  protected openChat(c: ChatMeta): void {
    this.chat.openChat(c.id);
    void this.router.navigate(['/tabs', 'chat', c.id]);
  }
}
