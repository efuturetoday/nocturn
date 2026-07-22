import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonHeader, IonToolbar, IonTitle, IonContent, IonButtons, IonButton, IonList, IonItem,
  IonLabel, IonChip, ModalController,
} from '@ionic/angular/standalone';
import { ApprovalService } from '../../core/services/approval.service';
import { ChatListService } from '../../core/services/chat-list.service';
import { ChatService } from '../../core/services/chat.service';

/**
 * The out-of-band approval REVEAL overlay — the app-global sheet auto-presented by
 * ApprovalPromptService whenever an approval awaits, so a request raised while you are anywhere
 * (another chat, another tab, or by a background agent run) is answerable. Reads
 * `approval.pending()` live: new requests appear, resolved ones vanish, and the presenter
 * auto-dismisses when the list empties. Each entry shows its provenance — the chat/agent run that
 * raised it — so the decision is informed, not a bare effect string.
 */
@Component({
  selector: 'app-approval-sheet',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    IonHeader, IonToolbar, IonTitle, IonContent, IonButtons, IonButton, IonList, IonItem,
    IonLabel, IonChip,
  ],
  template: `
    <ion-header>
      <ion-toolbar>
        <ion-title>Approval needed</ion-title>
        <ion-buttons slot="end"><ion-button (click)="close()">Close</ion-button></ion-buttons>
      </ion-toolbar>
    </ion-header>

    <ion-content class="ion-padding">
      <ion-list inset="true">
        @for (a of approval.pending(); track a.id) {
          <ion-item lines="full">
            <ion-label class="wrap">
              @if (origin(a.chatId); as o) {
                <button type="button" class="origin" (click)="openOrigin(a.chatId!)">
                  <ion-chip [color]="o.source === 'agent' ? 'tertiary' : 'primary'">{{ o.source === 'agent' ? 'Agent' : 'Chat' }}</ion-chip>
                  <span class="origin-name">{{ o.name }}</span>
                </button>
              }
              <h2>{{ a.intent }}</h2>
              <div class="actions">
                @for (opt of a.options; track $index) {
                  <ion-button size="small" fill="solid" (click)="approval.resolve(a.id, $index)">{{ opt }}</ion-button>
                }
                <ion-button size="small" fill="outline" color="medium" (click)="approval.resolve(a.id, -1)">Deny</ion-button>
              </div>
            </ion-label>
          </ion-item>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No approvals waiting.</ion-label></ion-item>
        }
      </ion-list>
    </ion-content>
  `,
  styles: `
    .wrap { white-space: normal; }
    h2 { font-family: var(--ion-font-family-monospace, monospace); margin: 0.25rem 0 0.5rem; }
    .origin { display: inline-flex; align-items: center; gap: 0.25rem; background: none; border: 0; padding: 0; margin: 0.25rem 0 0; cursor: pointer; }
    .origin-name { color: var(--ion-color-medium); font-size: 0.85rem; }
    .actions { display: flex; flex-wrap: wrap; gap: 0.5rem; margin: 0.25rem 0 0.5rem; }
  `,
})
export class ApprovalSheetPage {
  protected readonly approval = inject(ApprovalService);
  private readonly chatList = inject(ChatListService);
  private readonly chat = inject(ChatService);
  private readonly router = inject(Router);
  private readonly modalCtrl = inject(ModalController);

  /** Resolve a chat id to a display origin (name + kind) from the loaded chat list. Absent id → no
   * provenance; a known id not in the list → best-effort label (a background/other-workspace run). */
  protected origin(chatId?: string): { name: string; source: string } | null {
    if (!chatId) return null;
    const c = this.chatList.chats().find((x) => x.id === chatId);
    if (c) return { name: c.name || 'Untitled', source: c.source };
    return { name: 'Background run', source: 'agent' };
  }

  /** Open the raising chat so the user can read the context before deciding, then close the sheet. */
  protected openOrigin(chatId: string): void {
    this.chat.openChat(chatId);
    void this.router.navigate(['/chat', chatId]);
    this.close();
  }

  protected close(): void {
    void this.modalCtrl.dismiss();
  }
}
