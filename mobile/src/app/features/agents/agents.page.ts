import { Component, ChangeDetectionStrategy, inject, computed } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonContent,
  IonList,
  IonListHeader,
  IonItem,
  IonLabel,
  IonNote,
  IonBadge,
  IonButton,
} from '@ionic/angular/standalone';
import { LucidePlay } from '@lucide/angular';
import { AgentService } from '../../core/services/agent.service';
import { ChatListService } from '../../core/services/chat-list.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import { ChatRowComponent } from '../chat/components/chat-row';
import type { AgentInfo, ChatMeta } from '../../core/protocol/nocturn-protocol';

/**
 * Agents tab. Two sections: the ROSTER of declared agents (name, schedule, autonomy, fire button) and
 * the RUNS they have produced (chats an agent owns, source === 'agent'). A run row is the same
 * app-chat-row the Chat list and Home use — one source of truth for a chat row, unread dot included.
 * Firing is fire-and-forget — the run appears under Runs via chat.activity; tapping a run opens it
 * (the reused ChatPage binds AgentRunService via the agents/run route, so it streams like any chat).
 */
@Component({
  selector: 'app-agents',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    WorkspaceHeaderComponent,
    ChatRowComponent,
    LucidePlay,
    IonContent,
    IonList,
    IonListHeader,
    IonItem,
    IonLabel,
    IonNote,
    IonBadge,
    IonButton,
  ],
  template: `
    <app-workspace-header />

    <ion-content class="ion-padding-vertical">
      <ion-list inset="true">
        <ion-list-header><ion-label>Agents</ion-label></ion-list-header>
        @for (a of agents(); track a.name) {
          <ion-item>
            <ion-label>
              <h3>{{ a.name }}</h3>
              @if (a.description) { <p>{{ a.description }}</p> }
              <ion-note>
                {{ a.when || 'manual' }}
                <ion-badge [color]="a.autonomy === 'guarded' ? 'warning' : 'medium'">{{ a.autonomy }}</ion-badge>
              </ion-note>
            </ion-label>
            <ion-button slot="end" fill="clear" (click)="fire(a)" [attr.aria-label]="'Run ' + a.name">
              <svg lucidePlay [size]="20" />
            </ion-button>
          </ion-item>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No agents declared.</ion-label></ion-item>
        }
      </ion-list>

      <ion-list inset="true">
        <ion-list-header><ion-label>Runs</ion-label></ion-list-header>
        @for (r of runs(); track r.id) {
          <ion-item button detail="false" (click)="openRun(r)">
            <app-chat-row
              [chat]="r"
              [unread]="chatList.unreadIds().has(r.id)"
              [approval]="chatList.approvalWaiting().has(r.id)"
            />
          </ion-item>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No agent runs yet.</ion-label></ion-item>
        }
      </ion-list>
    </ion-content>
  `,
})
export class AgentsPage {
  private readonly agentsSvc = inject(AgentService);
  protected readonly chatList = inject(ChatListService);
  private readonly router = inject(Router);

  protected readonly agents = this.agentsSvc.agents;
  protected readonly runs = computed(() =>
    // Date.parse, not a string compare — see the same note in shell.page.ts: `updated` is RFC3339
    // with an offset, so lexical order is not chronological order.
    [...this.chatList.chats()]
      .filter((c) => c.source === 'agent')
      .sort((a, b) => Date.parse(b.updated) - Date.parse(a.updated)),
  );

  /** Trigger a run now (fire-and-forget); it surfaces under Runs when it starts. */
  protected fire(a: AgentInfo): void {
    this.agentsSvc.fire(a.name);
  }

  protected openRun(r: ChatMeta): void {
    (document.activeElement as HTMLElement | null)?.blur();
    void this.router.navigate(['/app/agents', 'run', r.id]); // ChatPage opens it (kind "agent") from the route
  }
}
