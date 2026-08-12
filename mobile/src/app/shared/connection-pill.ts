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
      <div
        class="conn-pill"
        [class.warn]="connection.state() !== 'disconnected'"
        role="status"
        aria-live="polite"
      >
        {{ connection.state() === 'disconnected' ? 'Disconnected' : 'Reconnecting…' }}
      </div>
    }
  `,
  styles: `
    /* Clears the composer bar (~56px) and the home indicator under it; the keyboard lift rides on
       top as a transform. A transform rather than an animated bottom, for the same reason the
       composer uses one: animating bottom runs layout and paint every frame, and this moves on the
       same 250ms curve as the composer beside it. */
    .conn-pill {
      position: fixed;
      left: 50%;
      bottom: calc(56px + var(--ion-safe-area-bottom, 0px));
      transform: translateX(-50%)
        translateY(calc(-1 * max(0px, var(--kb-height, 0px) - var(--ion-safe-area-bottom, 0px))));
      transition: transform 0.25s ease-out;
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
    .conn-pill.warn {
      background: var(--ion-color-warning);
      color: var(--ion-color-warning-contrast);
    }
  `,
})
export class ConnectionPillComponent {
  protected readonly connection = inject(ConnectionService);
}
