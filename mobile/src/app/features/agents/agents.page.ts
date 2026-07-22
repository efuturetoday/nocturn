import { Component, ChangeDetectionStrategy, inject, computed } from '@angular/core';
import { Router } from '@angular/router';
import { DatePipe } from '@angular/common';
import { IonContent, IonList, IonItem, IonLabel, IonIcon, IonNote } from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { chatbubbleOutline } from 'ionicons/icons';
import { ChatService } from '../../core/services/chat.service';
import { ChatListService } from '../../core/services/chat-list.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import type { ChatMeta } from '../../core/protocol/nocturn-protocol';

/**
 * Agents tab. Lists the agent runs — chats an agent owns (source === 'agent'), newest first — so
 * scheduled/background agent activity surfaces here. Tapping a run opens that chat. (Per-agent
 * grouping + live scheduler state need a server agent wire, a later slice.)
 */
@Component({
  selector: 'app-agents',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [DatePipe, WorkspaceHeaderComponent, IonContent, IonList, IonItem, IonLabel, IonIcon, IonNote],
  template: `
    <app-workspace-header />

    <ion-content class="ion-padding-vertical">
      <ion-list inset="true">
        @for (r of runs(); track r.id) {
          <ion-item button detail="true" (click)="openChat(r)">
            <ion-icon slot="start" name="chatbubble-outline" color="medium" aria-hidden="true" />
            <ion-label>
              <h3>{{ r.name || 'Run' }}</h3>
              <ion-note>{{ r.turns }} msg · {{ r.updated | date: 'short' }}</ion-note>
            </ion-label>
          </ion-item>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No agent runs yet.</ion-label></ion-item>
        }
      </ion-list>
    </ion-content>
  `,
})
export class AgentsPage {
  private readonly chat = inject(ChatService);
  private readonly chatList = inject(ChatListService);
  private readonly router = inject(Router);

  protected readonly runs = computed(() =>
    [...this.chatList.chats()].filter((c) => c.source === 'agent').sort((a, b) => b.updated.localeCompare(a.updated)),
  );

  constructor() {
    addIcons({ chatbubbleOutline });
  }

  protected openChat(c: ChatMeta): void {
    (document.activeElement as HTMLElement | null)?.blur();
    this.chat.openChat(c.id);
    void this.router.navigate(['/agents', 'run', c.id]);
  }
}
