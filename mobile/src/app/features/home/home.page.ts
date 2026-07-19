import { Component, ChangeDetectionStrategy, inject, computed } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonContent, IonCard, IonCardContent, IonList, IonItem, IonLabel, IonIcon,
  IonButton, IonChip, IonListHeader,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { addOutline } from 'ionicons/icons';
import { WorkspaceService } from '../../core/services/workspace.service';
import { ChatService } from '../../core/services/chat.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import { ChatRowComponent } from '../chat/components/chat-row';
import type { ChatMeta } from '../../core/protocol/nocturn-protocol';

@Component({
  selector: 'app-home',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    WorkspaceHeaderComponent, ChatRowComponent, IonContent, IonCard, IonCardContent, IonList,
    IonItem, IonLabel, IonIcon, IonButton, IonChip, IonListHeader,
  ],
  template: `
    <app-workspace-header />

    <ion-content class="ion-padding">
      <ion-card>
        <ion-card-content>
          @if (detail(); as d) {
            <ion-chip color="tertiary">{{ d.agents.length }} agents</ion-chip>
            <ion-chip color="tertiary">{{ d.skills.length }} skills</ion-chip>
            @if (d.persona) { <ion-chip color="secondary">persona set</ion-chip> }
          }
          <div class="actions">
            <ion-button size="small" (click)="go('chat')">
              <ion-icon slot="start" name="add-outline" /> New chat
            </ion-button>
          </div>
        </ion-card-content>
      </ion-card>

      <ion-list>
        <ion-list-header><ion-label>Recent chats</ion-label></ion-list-header>
        @for (c of recent(); track c.id) {
          <ion-item button detail="false" (click)="openChat(c)">
            <app-chat-row
              [chat]="c"
              [unread]="chat.unread().has(c.id)"
              [approval]="chat.approvalWaiting().has(c.id)"
            />
          </ion-item>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No chats yet.</ion-label></ion-item>
        }
      </ion-list>

      <!-- Reminders section lands here once the daemon exposes a reminders event. -->
    </ion-content>
  `,
  styles: `
    .actions { display: flex; gap: 8px; margin-top: 10px; }
    ion-card-title { display: flex; align-items: center; gap: 8px; justify-content: space-between; }
  `,
})
export class HomePage {
  protected readonly workspaces = inject(WorkspaceService);
  protected readonly chat = inject(ChatService);
  private readonly router = inject(Router);

  protected readonly detail = this.workspaces.selected;
  protected readonly recent = computed(() =>
    [...this.chat.chats()]
      .filter((c) => !c.agent)
      .sort((a, b) => b.updated.localeCompare(a.updated))
      .slice(0, 5),
  );

  constructor() {
    addIcons({ addOutline });
  }

  protected openChat(c: ChatMeta): void {
    this.chat.openChat(c.id);
    void this.router.navigate(['/tabs', 'chat', c.id]);
  }

  protected go(tab: string): void {
    void this.router.navigate(['/tabs', tab]);
  }
}
