import { Component, ChangeDetectionStrategy, inject, effect, computed, viewChild, ElementRef } from '@angular/core';
import { Router } from '@angular/router';
import { IonContent, IonItem, IonLabel, IonFooter, type ViewWillLeave } from '@ionic/angular/standalone';
import { ChatService } from '../../core/services/chat.service';
import { ChatListService } from '../../core/services/chat-list.service';
import { ConnectionService } from '../../core/services/connection.service';
import { KeyboardService } from '../../core/services/keyboard.service';
import { WorkspaceService } from '../../core/services/workspace.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import { ComposerComponent } from '../../shared/composer';
import { KbFollowDirective } from '../../shared/kb-follow.directive';
import { ChatRowComponent } from '../chat/components/chat-row';
import type { ChatMeta } from '../../core/protocol/nocturn-protocol';

/**
 * The Chat tab — a messaging-style list: newest chat at the BOTTOM (older scrolling up), with the
 * "ask Nocturn" composer docked at the bottom (shared ComposerComponent, identical to the chat
 * detail's input). Submitting starts a fresh chat and navigates to it. With `resize: native` the
 * WebView shrinks for the keyboard, so ion-footer rides above it on its own.
 *
 * Bottom-anchoring is done in CSS, not JS: `::part(scroll)` is a `column-reverse` flex, so the browser
 * rests at the bottom and keeps new items (prepended in DOM order = descending) pinned there — no
 * scrollToBottom, no scroll-position bookkeeping.
 */
@Component({
  selector: 'app-chats',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    WorkspaceHeaderComponent, ChatRowComponent, ComposerComponent, KbFollowDirective,
    IonContent, IonItem, IonLabel, IonFooter,
  ],
  template: `
    <app-workspace-header />

    <ion-content
      #content
      class="reverse"
      [style.--padding-top.px]="12"
      [style.--padding-bottom.px]="kb.height() + 12"
      [scrollEvents]="true"
      (ionScroll)="onScroll()"
    >
      @for (c of sorted(); track c.id) {
        <ion-item button detail="false" (click)="openChat(c)">
          <app-chat-row
            [chat]="c"
            [unread]="chatList.unreadIds().has(c.id)"
            [approval]="chatList.approvalWaiting().has(c.id)"
          />
        </ion-item>
      } @empty {
        <ion-item lines="none"><ion-label color="medium">No chats yet — ask something below.</ion-label></ion-item>
      }
    </ion-content>

    <ion-footer #footer kbFollow>
      <app-composer placeholder="Ask Nocturn…" [disabled]="!connection.connected()" (send)="startChat($event)" />
    </ion-footer>
  `,
  styles: `
    /* Invert the scroll: the scroller lays its items bottom-up and rests at the bottom (newest),
       so the list opens at the newest with no JS. Items are rendered newest-first; this flips them. */
    ion-content.reverse::part(scroll) {
      display: flex;
      flex-direction: column-reverse;
    }
    /* The items are now flex children — pin their natural height, else flex-shrink squeezes/clips
       them (cut-off rows). Stretch keeps full width. */
    ion-content.reverse ion-item {
      flex-shrink: 0;
      width: 100%;
    }
  `,
})
export class ChatsPage implements ViewWillLeave {
  protected readonly chat = inject(ChatService);
  protected readonly chatList = inject(ChatListService);
  protected readonly connection = inject(ConnectionService);
  protected readonly kb = inject(KeyboardService);
  protected readonly workspaces = inject(WorkspaceService);
  private readonly router = inject(Router);

  private readonly content = viewChild.required<IonContent>('content');
  private readonly contentEl = viewChild.required('content', { read: ElementRef });
  private readonly footer = viewChild.required('footer', { read: ElementRef });
  private lastScrollTop = 0;

  // User chats only (agent runs live under Agents). NEWEST-FIRST in the DOM; column-reverse (see the
  // styles) puts the newest visually at the bottom.
  protected readonly sorted = computed(() =>
    [...this.chatList.chats()].filter((c) => c.source !== 'agent').sort((a, b) => b.updated.localeCompare(a.updated)),
  );

  constructor() {
    // Load the active workspace's chats whenever it resolves/changes.
    effect(() => {
      if (this.workspaces.active()) this.chatList.listChats();
    });
  }

  // Collapse the keyboard-lift the instant we leave (into a chat) — the leaving OnPush view isn't
  // change-detected during the slide, so a nav with the keyboard open would leave the footer transform
  // + content padding stale (a blank filler). Reset imperatively (CD-independent); re-applied on
  // re-enter. RESET, not hide, so a canceled swipe-back keeps a normal composer.
  ionViewWillLeave(): void {
    const f = this.footer().nativeElement as HTMLElement;
    f.classList.remove('kb-follow'); // make it a vanilla footer for the slide (no compositor pin)
    f.style.removeProperty('transform');
    f.style.removeProperty('--kb-fill');
    (this.contentEl().nativeElement as HTMLElement).style.removeProperty('--padding-bottom');
  }

  /** A scroll gesture on the list dismisses an open keyboard (direction-agnostic — the scroll is
   * inverted by column-reverse, so a plain delta check is the robust signal). */
  protected async onScroll(): Promise<void> {
    const el = await this.content().getScrollElement();
    if (this.kb.open() && Math.abs(el.scrollTop - this.lastScrollTop) > 4) this.kb.dismiss();
    this.lastScrollTop = el.scrollTop;
  }

  /** Start a fresh chat from the composer: mint a client id, queue the first message, navigate. */
  protected startChat(text: string): void {
    const q = text.trim();
    if (!q) return;
    const id = this.chat.newChat();
    this.chat.queueFirstMessage(q);
    void this.router.navigate(['/chat', id]);
  }

  protected openChat(c: ChatMeta): void {
    (document.activeElement as HTMLElement | null)?.blur();
    void this.router.navigate(['/chat', c.id]); // ChatPage opens it from the route (kind "user")
  }
}
