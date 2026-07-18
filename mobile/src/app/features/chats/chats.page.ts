import { Component, ChangeDetectionStrategy, inject, input, effect, signal, computed } from '@angular/core';
import { Router } from '@angular/router';
import { DatePipe } from '@angular/common';
import {
  IonHeader, IonToolbar, IonTitle, IonContent, IonList, IonItem, IonLabel, IonButtons,
  IonBackButton, IonFab, IonFabButton, IonIcon, IonNote, IonBadge, IonItemSliding,
  IonItemOptions, IonItemOption, AlertController,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { addOutline, createOutline, trashOutline, chatbubbleOutline } from 'ionicons/icons';
import { ChatService } from '../../core/services/chat.service';
import type { ChatMeta } from '../../core/protocol/nocturn-protocol';

@Component({
  selector: 'app-chats',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    DatePipe, IonHeader, IonToolbar, IonTitle, IonContent, IonList, IonItem, IonLabel,
    IonButtons, IonBackButton, IonFab, IonFabButton, IonIcon, IonNote, IonBadge,
    IonItemSliding, IonItemOptions, IonItemOption,
  ],
  template: `
    <ion-header>
      <ion-toolbar>
        <ion-buttons slot="start"><ion-back-button defaultHref="/workspaces" /></ion-buttons>
        <ion-title>{{ ws() }}</ion-title>
      </ion-toolbar>
    </ion-header>

    <ion-content>
      <ion-list>
        @for (c of sorted(); track c.id) {
          <ion-item-sliding>
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
                <ion-badge slot="end" color="primary" aria-label="unread">●</ion-badge>
              }
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
          <ion-item lines="none"><ion-label color="medium">No chats yet — tap + to start one.</ion-label></ion-item>
        }
      </ion-list>

      <ion-fab slot="fixed" vertical="bottom" horizontal="end">
        <ion-fab-button (click)="startNew()"><ion-icon name="add-outline" /></ion-fab-button>
      </ion-fab>
    </ion-content>
  `,
})
export class ChatsPage {
  /** Bound from the `:ws` route param via withComponentInputBinding(). */
  readonly ws = input.required<string>();

  protected readonly chat = inject(ChatService);
  private readonly router = inject(Router);
  private readonly alerts = inject(AlertController);

  private readonly knownIds = signal<Set<string> | null>(null); // set while awaiting a new chat
  protected readonly sorted = computed(() => [...this.chat.chats()].sort((a, b) => b.updated.localeCompare(a.updated)));

  constructor() {
    addIcons({ addOutline, createOutline, trashOutline, chatbubbleOutline });
    // Load this workspace's chats whenever the bound param resolves/changes.
    effect(() => {
      const w = this.ws();
      if (w) this.chat.listChats(w);
    });
    // After a newChat, the server echoes an updated list; jump into the id we didn't have before.
    effect(() => {
      const before = this.knownIds();
      if (!before) return;
      const fresh = this.chat.chats().find((c) => !before.has(c.id));
      if (fresh) {
        this.knownIds.set(null);
        this.openChat(fresh);
      }
    });
  }

  protected openChat(c: ChatMeta): void {
    this.blur();
    void this.router.navigate([this.ws(), 'chat', c.id]);
  }

  protected async startNew(): Promise<void> {
    const alert = await this.alerts.create({
      header: 'New chat',
      inputs: [{ name: 'name', type: 'text', placeholder: 'Chat name', value: 'New chat' }],
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        {
          text: 'Create',
          handler: (v) => {
            const name = (v.name ?? '').trim() || 'New chat';
            this.knownIds.set(new Set(this.chat.chats().map((c) => c.id)));
            this.chat.newChat(this.ws(), name);
          },
        },
      ],
    });
    await alert.present();
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

  /** Drop focus before a route change so Ionic's aria-hidden on the leaving page is valid. */
  private blur(): void {
    (document.activeElement as HTMLElement | null)?.blur();
  }
}
