import { inject } from '@angular/core';
import { CanActivateFn, Router } from '@angular/router';
import { ConnectionService } from '../services/connection.service';
import { DiscoveryService } from '../services/discovery.service';
import { AuthService } from '../services/auth.service';

/**
 * connectionGuard protects the tabs/chat routes: on a fresh load the in-memory connection is gone,
 * so it re-dials the persisted last host WITH its stored bearer (auto-reconnect on reload). No host
 * → Discover; a known host but no bearer (this device isn't paired) → the Pair screen. It doesn't
 * block on the socket opening — services resync via effect once state flips to 'connected'.
 */
export const connectionGuard: CanActivateFn = async () => {
  const conn = inject(ConnectionService);
  const discovery = inject(DiscoveryService);
  const auth = inject(AuthService);
  const router = inject(Router);

  if (conn.state() !== 'disconnected') return true;

  const last = await discovery.lastHostValue();
  if (!last) return router.createUrlTree(['/discover']);

  const bearer = await auth.bearerFor(last);
  if (!bearer) return router.createUrlTree(['/discover']); // not paired → Discover opens the pairing overlay

  conn.connect(last, bearer);
  return true;
};
