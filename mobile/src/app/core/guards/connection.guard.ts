import { inject } from '@angular/core';
import { CanActivateFn, Router } from '@angular/router';
import { ConnectionService } from '../services/connection.service';
import { DiscoveryService } from '../services/discovery.service';
import { DaemonService } from '../services/daemon.service';
import { AuthService } from '../services/auth.service';

/**
 * connectionGuard protects the tabs/chat routes: on a fresh load the in-memory connection is gone,
 * so it re-dials WITH the stored bearer (auto-reconnect on reload). No host → Discover; a known host
 * but no bearer (this device isn't paired) → Discover, which opens the pairing overlay. It doesn't
 * block on the socket opening — services resync via effect once state flips to 'connected'.
 *
 * Which host it re-dials depends on how the app was loaded. A browser served BY a daemon has exactly
 * one candidate — the origin it came from — and asking it to remember a "last host" would be
 * theatre: it cannot be pointed anywhere else, and a stale entry from a previous LAN session would
 * send it to a daemon it is not being served by. The native app has no such origin, so it keeps
 * using the persisted host.
 */
export const connectionGuard: CanActivateFn = async () => {
  const conn = inject(ConnectionService);
  const discovery = inject(DiscoveryService);
  const daemon = inject(DaemonService);
  const auth = inject(AuthService);
  const router = inject(Router);

  if (conn.state() !== 'disconnected') return true;

  await daemon.probeLocal();
  const host = daemon.sameOrigin ? daemon.localUrl() : await discovery.lastHostValue();
  if (!host) return router.createUrlTree(['/discover']);

  const bearer = await auth.bearerFor(host);
  if (!bearer) return router.createUrlTree(['/discover']); // not paired → Discover opens the pairing overlay

  conn.connect(host, bearer);
  return true;
};
