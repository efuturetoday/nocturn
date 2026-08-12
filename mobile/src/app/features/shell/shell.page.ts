import { Component, ChangeDetectionStrategy, inject, effect, computed } from '@angular/core';
import { Router, RouterLink, RouterLinkActive } from '@angular/router';
import {
  IonSplitPane, IonMenu, IonMenuToggle, IonRouterOutlet, IonHeader, IonToolbar, IonTitle,
  IonContent, IonList, IonListHeader, IonItem, IonLabel, IonBadge, NavController,
} from '@ionic/angular/standalone';
import {
  LucideAlarmClock, LucideBot, LucideSettings, LucidePlus, LucideSparkles, LucideStore,
} from '@lucide/angular';
import { ChatListService } from '../../core/services/chat-list.service';
import { ReminderService } from '../../core/services/reminder.service';
import { WorkspaceService } from '../../core/services/workspace.service';
import { ChatRowComponent } from '../chat/components/chat-row';
import { chatToHero } from '../../shared/hero-transition';
import type { ChatMeta } from '../../core/protocol/nocturn-protocol';

/**
 * The app shell: a left ion-menu drawer plus the outlet every page routes into. It replaces the
 * bottom tab bar — which had to hide itself whenever the keyboard opened — so the bottom edge now
 * belongs to the keyboard alone.
 *
 * The drawer is Ionic's, not ours: ion-menu handles the overlay, the scrim, the edge-swipe and the
 * back-button integration, and ion-split-pane pins it open from `lg` up (a tablet gets a permanent
 * sidebar for free). Every navigating row sits in an ion-menu-toggle, which is how a menu closes
 * itself — no MenuController calls.
 *
 * The drawer's lower half IS the chat list, so there is no separate list page: the chats live one
 * gesture away from wherever you are, including from inside another chat.
 */
@Component({
  selector: 'app-shell',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    RouterLink, RouterLinkActive, ChatRowComponent,
    IonSplitPane, IonMenu, IonMenuToggle, IonRouterOutlet, IonHeader, IonToolbar, IonTitle,
    IonContent, IonList, IonListHeader, IonItem, IonLabel, IonBadge,
    LucideAlarmClock, LucideBot, LucideSettings, LucidePlus, LucideSparkles, LucideStore,
  ],
  template: `
    <ion-split-pane contentId="main" when="lg">
      <ion-menu contentId="main" menuId="main" side="start" type="overlay">
        <ion-header>
          <ion-toolbar>
            <!-- The wordmark is one inline-flex box INSIDE ion-title: ion-title's own shadow wrapper
                 is a block, so flexing the host alone drops the mark onto its own line. -->
            <ion-title>
              <span class="wordmark">
                <svg lucideSparkles [size]="20" class="mark" />
                nocturn
              </span>
            </ion-title>
          </ion-toolbar>
        </ion-header>

        <ion-content>
          <!-- The destinations. Counts ride along so the drawer answers "is anything waiting?"
               without being opened twice. -->
          <ion-list lines="none">
            <ion-menu-toggle [autoHide]="false">
              <ion-item button detail="false" routerLink="/app/reminders" routerLinkActive="nav-on">
                <svg lucideAlarmClock slot="start" [size]="21" />
                <ion-label>Reminders</ion-label>
                @if (reminders.count()) {
                  <ion-badge slot="end" color="primary">{{ reminders.count() }}</ion-badge>
                }
              </ion-item>
            </ion-menu-toggle>

            <ion-menu-toggle [autoHide]="false">
              <ion-item button detail="false" routerLink="/app/agents" routerLinkActive="nav-on">
                <svg lucideBot slot="start" [size]="21" />
                <ion-label>Agents</ion-label>
                @if (chatList.unreadAgentCount() > 0) {
                  <ion-badge slot="end" color="tertiary">{{ chatList.unreadAgentCount() }}</ion-badge>
                }
              </ion-item>
            </ion-menu-toggle>

            <ion-menu-toggle [autoHide]="false">
              <ion-item button detail="false" routerLink="/app/library" routerLinkActive="nav-on">
                <svg lucideStore slot="start" [size]="21" />
                <ion-label>Library</ion-label>
              </ion-item>
            </ion-menu-toggle>

            <!-- Skills, MCP and Workspaces are tabs inside Settings: the list below this is the chat
                 history, and every row here pushes it further down. -->
            <ion-menu-toggle [autoHide]="false">
              <ion-item button detail="false" routerLink="/app/settings" routerLinkActive="nav-on">
                <svg lucideSettings slot="start" [size]="21" />
                <ion-label>Settings</ion-label>
              </ion-item>
            </ion-menu-toggle>
          </ion-list>

          <div class="rule" aria-hidden="true"></div>

          <ion-list lines="none">
            <ion-menu-toggle [autoHide]="false">
              <ion-item button detail="false" class="new-chat" (click)="newChat()">
                <svg lucidePlus slot="start" [size]="21" />
                <ion-label>New chat</ion-label>
              </ion-item>
            </ion-menu-toggle>
          </ion-list>

          <ion-list class="chats">
            <ion-list-header><ion-label>Chats</ion-label></ion-list-header>
            @for (c of chats(); track c.id) {
              <ion-menu-toggle [autoHide]="false">
                <ion-item button detail="false" (click)="openChat(c)">
                  <app-chat-row
                    [chat]="c"
                    [unread]="chatList.unreadIds().has(c.id)"
                    [approval]="chatList.approvalWaiting().has(c.id)"
                  />
                </ion-item>
              </ion-menu-toggle>
            } @empty {
              <ion-item lines="none"><ion-label color="medium">No chats yet.</ion-label></ion-item>
            }
          </ion-list>
        </ion-content>
      </ion-menu>

      <!-- swipeGesture off. The left screen edge can only belong to one gesture, and here it belongs
           to the drawer: ion-menu's edge-swipe and the outlet's iOS swipe-to-go-back both listen
           there, so one drag opened the menu AND popped the page behind it back to the hero. The
           drawer is this app's navigation — it holds the history and every destination — so the back
           gesture is the one that goes. Nothing depends on it: the chat has no back button, and new
           chat navigates back explicitly. -->
      <ion-router-outlet id="main" [swipeGesture]="false" />
    </ion-split-pane>
  `,
  styles: `
    /* The drawer is a surface in its own right: the page floor, so an opening drawer reads as a
       layer sliding over the artwork rather than as another card. */
    ion-menu { --width: 304px; --background: var(--ion-background-color); }
    ion-menu ion-content { --background: var(--ion-background-color); --padding-top: 0.5rem; }

    /* Ionic gives EVERY ion-list 8px of vertical padding. The drawer stacks three of them, so that
       padding lands twice at each seam and every gap in here reads 16px larger than the source
       suggests. Zero it and let the margins below own the whole rhythm.
       Ionic's own .ion-no-padding utility does NOT do it: measured, the padding stays at 8px. The
       utility and Ionic's own list rule have the same specificity and the list stylesheet loads later,
       so the
       component wins. Hence a rule of ours, with the element in the selector to outrank it. */
    ion-menu ion-list { background: transparent; padding-block: 0; }

    /* No override on the backdrop. Pinning its opacity took a darker veil but cost the fade: the menu
       animation drives that opacity through the Web Animations API, an !important declaration beats
       an animation, so the scrim held full strength for the whole close and then vanished in one
       frame after it. Ionic's own value fades with the drawer, which is worth more than the depth. */

    /* A hairline under the header, which Ionic's toolbar leaves off by default — without it the
       drawer reads as one undifferentiated column. --border-style is the load-bearing one. */
    ion-menu ion-toolbar {
      --border-width: 0 0 1px 0;
      --border-style: solid;
      --border-color: var(--ion-border-color);
    }

    /* The wordmark: lowercase, display face (the global ion-title rule), sparkle in the accent. Its
       left edge is the drawer's, the same 1rem the rows below start from — the header is the top of
       one column, not a separate object. Ionic's own toolbar padding is zeroed so the title's
       padding is the only thing setting that edge. */
    ion-menu ion-toolbar { --padding-start: 0; --padding-end: 0; }
    ion-menu ion-title { padding-inline: 1rem; }
    ion-menu .wordmark {
      display: inline-flex; align-items: center; gap: 0.4rem;
      font-size: 1.35rem; font-weight: 500; letter-spacing: -0.03em;
    }
    /* Lucide draws in outline only — its svg carries fill="none". A CSS fill outranks that
       presentation attribute, and the paths inherit it, so the mark reads as a solid glyph beside
       the wordmark instead of a hollow one. */
    ion-menu .wordmark .mark { color: var(--ion-color-primary); fill: currentColor; }

    /* Destination rows sit closer together than a content list — they are a nav, not data.
       2.9rem = the icon's 24px plus 0.7rem of air above and below. */
    ion-menu ion-item { --min-height: 2.9rem; --background: transparent; }
    ion-menu ion-item > svg[slot='start'] { margin-inline-end: 0.9rem; color: var(--ion-color-medium); }

    /* The active destination. Tinted from the primary's own rgb token so it stays a wash of the
       accent rather than a second, hand-picked purple. */
    ion-menu ion-item.nav-on { --background: rgba(var(--ion-color-primary-rgb), 0.16); }
    ion-menu ion-item.nav-on > svg[slot='start'],
    ion-menu ion-item.nav-on ion-label { color: var(--ion-color-secondary); }

    /* New chat leads the history rather than joining the destinations, so it carries the accent. */
    ion-menu ion-item.new-chat > svg[slot='start'],
    ion-menu ion-item.new-chat ion-label { color: var(--ion-color-secondary); font-weight: 500; }

    .rule { height: 1px; margin: 0.6rem 1rem; background: var(--ion-border-color); }

    /* The history is data: full row height, hairlines between, the section header above it. The top
       margin is the beat that separates the history from New chat — without it the section header
       reads as a caption belonging to the button above it. */
    ion-menu ion-list.chats { margin-top: 0.9rem; }
    ion-menu ion-list.chats ion-item { --min-height: 3.25rem; }
    /* The gap under the header is the HEADER's margin, not the label's: a label margin is measured
       inside the header's own box, so it moves the text without moving the row below it. */
    ion-menu ion-list.chats ion-list-header {
      min-height: 0;
      margin-bottom: 0.35rem;
      padding-inline-start: 1rem;
      font-size: 0.8rem; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase;
    }
    ion-menu ion-list.chats ion-list-header ion-label {
      margin: 0;
      color: var(--ion-color-medium) !important;
    }
  `,
})
export class ShellPage {
  protected readonly chatList = inject(ChatListService);
  protected readonly reminders = inject(ReminderService);
  private readonly workspaces = inject(WorkspaceService);
  private readonly router = inject(Router);
  private readonly nav = inject(NavController);

  // User chats only, newest first — agent runs have their own home under Agents.
  //
  // Date.parse, not a string compare. `updated` is a Go time.Time marshalled as RFC3339, which
  // carries the daemon's offset — so a run of "+02:00" stamps sorts lexically among "+01:00" ones by
  // their digits rather than their instants, and the order silently scrambles across a DST change.
  protected readonly chats = computed(() =>
    [...this.chatList.chats()]
      .filter((c) => c.source !== 'agent')
      .sort((a, b) => Date.parse(b.updated) - Date.parse(a.updated)),
  );

  constructor() {
    // Load the active workspace's chats whenever it resolves/changes. The shell outlives every page,
    // so this is the one place the list has to be asked for.
    effect(() => {
      if (this.workspaces.active()) this.chatList.listChats();
    });
  }

  /** Same reversed camera move the chat's own plus button plays, so the drawer route feels the same
      as the in-page one. */
  protected newChat(): void {
    void this.nav.navigateBack(['/app/home'], { animation: chatToHero });
  }

  protected openChat(c: ChatMeta): void {
    (document.activeElement as HTMLElement | null)?.blur();
    void this.router.navigate(['/app/chat', c.id]); // ChatPage opens it from the route (kind "user")
  }
}
