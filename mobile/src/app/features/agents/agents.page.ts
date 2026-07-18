import { Component, ChangeDetectionStrategy, inject, computed, signal } from '@angular/core';
import { Router } from '@angular/router';
import { DatePipe } from '@angular/common';
import {
  IonContent, IonList, IonItem, IonLabel, IonIcon, IonNote, IonBadge,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { hardwareChipOutline, chatbubbleOutline, chevronForward, chevronDown } from 'ionicons/icons';
import { WorkspaceService } from '../../core/services/workspace.service';
import { ChatService } from '../../core/services/chat.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import type { ChatMeta } from '../../core/protocol/nocturn-protocol';

/**
 * Agents tab. Each declared agent lists its runs — the chats it owns (ChatMeta.agent === name),
 * newest first — so background/scheduled agent activity surfaces here, grouped per agent. Tapping
 * a run opens that chat. (Cron next-fire/live scheduler state still needs a server `jobs` wire.)
 */
@Component({
  selector: 'app-agents',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    DatePipe, WorkspaceHeaderComponent, IonContent, IonList, IonItem, IonLabel,
    IonIcon, IonNote, IonBadge,
  ],
  template: `
    <app-workspace-header />

    <ion-content class="ion-padding-vertical">
      @for (a of agents(); track a.name) {
        <ion-list inset="true">
          <ion-item button detail="false" lines="full" class="agent-head" (click)="toggle(a.name)">
            <ion-icon slot="start" name="hardware-chip-outline" color="tertiary" aria-hidden="true" />
            <ion-label class="ion-text-wrap">
              <h2>{{ a.name }}</h2>
              <ion-note>{{ a.description }}</ion-note>
            </ion-label>
            @if (runsOf(a.name).length) {
              <ion-badge slot="end" color="tertiary">{{ runsOf(a.name).length }}</ion-badge>
            }
            <ion-icon
              slot="end"
              [name]="isOpen(a.name) ? 'chevron-down' : 'chevron-forward'"
              color="medium"
              aria-hidden="true"
            />
          </ion-item>

          @if (isOpen(a.name)) {
            @for (r of runsOf(a.name); track r.id) {
              <ion-item button detail="true" (click)="openChat(r)">
                <ion-icon slot="start" name="chatbubble-outline" color="medium" aria-hidden="true" />
                <ion-label>
                  <h3>{{ r.name || 'Run' }}</h3>
                  <ion-note>{{ r.turns }} msg · {{ r.updated | date: 'short' }}</ion-note>
                </ion-label>
              </ion-item>
            } @empty {
              <ion-item lines="none"><ion-label color="medium"><p>No runs yet.</p></ion-label></ion-item>
            }
          }
        </ion-list>
      } @empty {
        <ion-list inset="true">
          <ion-item lines="none"><ion-label color="medium">No agents in this workspace.</ion-label></ion-item>
        </ion-list>
      }
    </ion-content>
  `,
  styles: `.agent-head h2 { font-weight: 700; } .agent-head { --background: var(--ion-color-step-100); }`,
})
export class AgentsPage {
  private readonly workspaces = inject(WorkspaceService);
  private readonly chat = inject(ChatService);
  private readonly router = inject(Router);

  protected readonly agents = () => this.workspaces.selected()?.agents ?? [];

  // chat.agent → its runs, newest first. Recomputed when the chat list changes.
  private readonly runsByAgent = computed(() => {
    const map = new Map<string, ChatMeta[]>();
    for (const c of this.chat.chats()) {
      if (!c.agent) continue;
      (map.get(c.agent) ?? map.set(c.agent, []).get(c.agent)!).push(c);
    }
    for (const list of map.values()) list.sort((a, b) => b.updated.localeCompare(a.updated));
    return map;
  });

  private readonly expanded = signal<ReadonlySet<string>>(new Set()); // collapsed by default

  constructor() {
    addIcons({ hardwareChipOutline, chatbubbleOutline, chevronForward, chevronDown });
  }

  protected runsOf(name: string): ChatMeta[] {
    return this.runsByAgent().get(name) ?? [];
  }

  protected isOpen(name: string): boolean {
    return this.expanded().has(name);
  }

  protected toggle(name: string): void {
    this.expanded.update((s) => {
      const next = new Set(s);
      next.has(name) ? next.delete(name) : next.add(name);
      return next;
    });
  }

  protected openChat(c: ChatMeta): void {
    (document.activeElement as HTMLElement | null)?.blur();
    this.chat.openChat(c.id);
    // Open in the Agents tab's own stack so Back returns here, not to the Chat tab.
    void this.router.navigate(['/tabs', 'agents', 'run', c.id]);
  }
}
