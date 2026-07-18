import { Component, ChangeDetectionStrategy, inject, computed } from '@angular/core';
import { Router } from '@angular/router';
import { DatePipe } from '@angular/common';
import {
  IonContent, IonCard, IonCardContent, IonList, IonItem, IonLabel, IonIcon, IonNote, IonBadge,
  IonButton, IonChip, IonListHeader,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { ellipse, chatbubbleOutline, addOutline } from 'ionicons/icons';
import { WorkspaceService } from '../../core/services/workspace.service';
import { ChatService } from '../../core/services/chat.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import type { ChatMeta } from '../../core/protocol/nocturn-protocol';

@Component({
  selector: 'app-home',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    DatePipe, WorkspaceHeaderComponent, IonContent, IonCard, IonCardContent, IonList, IonItem,
    IonLabel, IonIcon, IonNote, IonBadge, IonButton, IonChip, IonListHeader,
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
          <ion-item button detail="true" (click)="openChat(c)">
            <ion-icon
              slot="start"
              name="chatbubble-outline"
              [color]="chat.unread().has(c.id) ? 'primary' : 'medium'"
              aria-hidden="true"
            />
            <ion-label>
              <h2>{{ c.name || 'Untitled chat' }}</h2>
              <ion-note>{{ c.turns }} msg · {{ c.updated | date: 'short' }}</ion-note>
            </ion-label>
            @if (chat.approvalWaiting().has(c.id)) {
              <ion-badge slot="end" color="warning">approval</ion-badge>
            } @else if (chat.unread().has(c.id)) {
              <ion-badge slot="end" color="primary">●</ion-badge>
            }
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
    addIcons({ ellipse, chatbubbleOutline, addOutline });
  }

  protected openChat(c: ChatMeta): void {
    this.chat.openChat(c.id);
    void this.router.navigate(['/tabs', 'chat', c.id]);
  }

  protected go(tab: string): void {
    void this.router.navigate(['/tabs', tab]);
  }
}
