import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import { IonTabs, IonTabBar, IonTabButton, IonIcon, IonLabel, IonBadge } from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { homeOutline, chatbubblesOutline, hardwareChipOutline, settingsOutline } from 'ionicons/icons';
import { ChatService } from '../../core/services/chat.service';
import { ConnectionService } from '../../core/services/connection.service';
import { KeyboardService } from '../../core/services/keyboard.service';

/**
 * The tab shell: Home · Chat · Agents · Settings. In Ionic 8 the router config is the single
 * source of truth for what each tab loads (no <ion-tab> needed); each tab keeps its own
 * navigation stack. A connection-status pill floats above the tab bar while not connected.
 */
@Component({
  selector: 'app-tabs',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonTabs, IonTabBar, IonTabButton, IonIcon, IonLabel, IonBadge],
  template: `
    <ion-tabs>
      @if (!connection.connected()) {
        <div class="conn-pill" [class.warn]="connection.state() !== 'disconnected'" [class.kb-open]="keyboard.open()">
          {{ connection.state() === 'disconnected' ? 'Disconnected' : 'Reconnecting…' }}
        </div>
      }

      <ion-tab-bar slot="bottom" [class.kb-hidden]="keyboard.open()">
        <ion-tab-button tab="home">
          <ion-icon name="home-outline" />
          <ion-label>Home</ion-label>
        </ion-tab-button>
        <ion-tab-button tab="chat">
          <ion-icon name="chatbubbles-outline" />
          <ion-label>Chat</ion-label>
          @if (chat.unreadUserCount() > 0) {
            <ion-badge color="primary">{{ chat.unreadUserCount() }}</ion-badge>
          }
        </ion-tab-button>
        <ion-tab-button tab="agents">
          <ion-icon name="hardware-chip-outline" />
          <ion-label>Agents</ion-label>
          @if (chat.unreadAgentCount() > 0) {
            <ion-badge color="tertiary">{{ chat.unreadAgentCount() }}</ion-badge>
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
    /* Fixed pill just above the tab bar (which is ~50px + the home-indicator inset). */
    .conn-pill {
      position: fixed;
      left: 50%;
      transform: translateX(-50%);
      bottom: calc(56px + var(--ion-safe-area-bottom, 0px));
      z-index: 20;
      padding: 0.3125rem 0.875rem;
      border-radius: 999px;
      font-size: 0.78rem;
      font-weight: 600;
      color: var(--ion-color-medium-contrast);
      background: var(--ion-color-medium);
      box-shadow: 0 0.25rem 0.875rem rgb(0 0 0 / 0.35);
      pointer-events: none;
    }
    .conn-pill.warn { background: var(--ion-color-warning); color: var(--ion-color-warning-contrast); }
    .conn-pill.kb-open { bottom: calc(8px + var(--ion-safe-area-bottom, 0px)); }

    /* Keyboard open → hide the tab bar outright (no animation) so the input docks on the keyboard. */
    ion-tab-bar.kb-hidden { display: none; }
  `,
})
export class TabsPage {
  protected readonly chat = inject(ChatService);
  protected readonly connection = inject(ConnectionService);
  protected readonly keyboard = inject(KeyboardService);

  constructor() {
    addIcons({ homeOutline, chatbubblesOutline, hardwareChipOutline, settingsOutline });
  }
}
