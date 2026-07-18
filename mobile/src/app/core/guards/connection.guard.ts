import { inject } from '@angular/core';
import { CanActivateFn, Router } from '@angular/router';
import { ConnectionService } from '../services/connection.service';
import { DiscoveryService } from '../services/discovery.service';

/**
 * connectionGuard protects the workspace/chat routes: on a fresh app load the in-memory
 * connection is gone, so it re-dials the persisted last host (auto-reconnect on reload). With
 * no known host it redirects to Discover. It does NOT block on the socket being open — the
 * services resync via `effect` once the state flips to 'connected'.
 */
export const connectionGuard: CanActivateFn = async () => {
  const conn = inject(ConnectionService);
  const discovery = inject(DiscoveryService);
  const router = inject(Router);

  if (conn.state() !== 'disconnected') return true;

  const last = await discovery.lastHostValue();
  if (last) {
    conn.connect(last);
    return true;
  }
  return router.createUrlTree(['/discover']);
};
