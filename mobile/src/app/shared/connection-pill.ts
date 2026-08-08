import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import { ConnectionService } from '../core/services/connection.service';

/**
 * The app-global connection-status pill: a fixed "Disconnected" / "Reconnecting…" chip shown on ANY
 * route while the socket is down. Mounted once in the app root, above the router outlet, so it floats
 * over whatever page is up. Purely presentational — reads the connection state and renders nothing
 * while connected.
 *
 * It knows nothing about the keyboard: --nocturn-bottom-inset already reports whatever occupies the
 * bottom edge, so the pill rides up with the keys by sitting on that.
 */
@Component({
  selector: 'app-connection-pill',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (!connection.connected()) {
      <div class="conn-pill" [class.warn]="connection.state() !== 'disconnected'">
        {{ connection.state() === 'disconnected' ? 'Disconnected' : 'Reconnecting…' }}
      </div>
    }
  `,
  styles: `
    /* Clears the composer bar (~56px) and whatever occupies the bottom edge under it. */
    .conn-pill {
      position: fixed;
      left: 50%;
      transform: translateX(-50%);
      bottom: calc(56px + var(--nocturn-bottom-inset, 0px));
      transition: bottom 0.25s ease-out;
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
  `,
})
export class ConnectionPillComponent {
  protected readonly connection = inject(ConnectionService);
}
