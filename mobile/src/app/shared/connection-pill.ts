import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import { ConnectionService } from '../core/services/connection.service';
import { KeyboardService } from '../core/services/keyboard.service';

/**
 * The app-global connection-status pill: a fixed "Disconnected" / "Reconnecting…" chip shown on ANY
 * route while the socket is down (not just the tab shell). Mounted once in the app root, above the
 * router outlet, so it floats over whatever page is up. Purely presentational — reads the connection
 * state + keyboard height and renders nothing while connected.
 */
@Component({
  selector: 'app-connection-pill',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (!connection.connected()) {
      <div class="conn-pill" [class.warn]="connection.state() !== 'disconnected'" [class.kb-open]="keyboard.open()">
        {{ connection.state() === 'disconnected' ? 'Disconnected' : 'Reconnecting…' }}
      </div>
    }
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
  `,
})
export class ConnectionPillComponent {
  protected readonly connection = inject(ConnectionService);
  protected readonly keyboard = inject(KeyboardService);
}
