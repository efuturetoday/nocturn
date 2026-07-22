import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import { IonTabs, IonTabBar, IonTabButton, IonIcon, IonLabel, IonBadge } from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { homeOutline, chatbubblesOutline, hardwareChipOutline, settingsOutline } from 'ionicons/icons';
import { ChatListService } from '../../core/services/chat-list.service';
import { KeyboardService } from '../../core/services/keyboard.service';

/**
 * The tab shell: Home · Chat · Agents · Settings. In Ionic 8 the router config is the single
 * source of truth for what each tab loads (no <ion-tab> needed); each tab keeps its own
 * navigation stack.
 */
@Component({
  selector: 'app-tabs',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonTabs, IonTabBar, IonTabButton, IonIcon, IonLabel, IonBadge],
  template: `
    <ion-tabs>
      <ion-tab-bar slot="bottom" [class.kb-hidden]="keyboard.open()">
        <ion-tab-button tab="home">
          <ion-icon name="home-outline" />
          <ion-label>Home</ion-label>
        </ion-tab-button>
        <ion-tab-button tab="chat">
          <ion-icon name="chatbubbles-outline" />
          <ion-label>Chat</ion-label>
          @if (chatList.unreadUserCount() > 0) {
            <ion-badge color="primary">{{ chatList.unreadUserCount() }}</ion-badge>
          }
        </ion-tab-button>
        <ion-tab-button tab="agents">
          <ion-icon name="hardware-chip-outline" />
          <ion-label>Agents</ion-label>
          @if (chatList.unreadAgentCount() > 0) {
            <ion-badge color="tertiary">{{ chatList.unreadAgentCount() }}</ion-badge>
          }
        </ion-tab-button>
        <ion-tab-button tab="settings">
          <ion-icon name="settings-outline" />
          <ion-label>Settings</ion-label>
        </ion-tab-button>
      </ion-tab-bar>
    </ion-tabs>
  `,
  styles: `
    /* Keyboard open → hide the tab bar outright (no animation) so the input docks on the keyboard. */
    ion-tab-bar.kb-hidden { display: none; }
  `,
})
export class TabsPage {
  protected readonly chatList = inject(ChatListService);
  protected readonly keyboard = inject(KeyboardService);

  constructor() {
    addIcons({ homeOutline, chatbubblesOutline, hardwareChipOutline, settingsOutline });
  }
}
