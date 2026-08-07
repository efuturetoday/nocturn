/**
 * The demo switch, and the one place that decides what "demo" means.
 *
 * App Review cannot reach a nocturn daemon: it runs on the user's own machine, on a network the
 * reviewer does not have, and there is no account to hand over instead. So the app carries a
 * self-contained demo — a scripted daemon behind the same wire protocol (see `demo-daemon.ts`),
 * which the REAL reducers render. Nothing about it is a parallel UI, so it cannot show something
 * the app itself could not.
 *
 * It is reached through the host name alone: the existing "Enter server manually" dialog builds
 * `ws://demo:8765/ws` from `demo`, and everything downstream — remembering the host, the
 * connection guard re-dialling it on a cold start — works unchanged. Nothing else marks the mode,
 * so there is no flag to leave set by accident.
 */

/** The host name that selects the demo instead of a real daemon. */
export const DEMO_HOST = 'demo';

/** The stand-in bearer for the demo. It never leaves the app — no socket, no fetch carries it. */
export const DEMO_BEARER = 'demo';

/** True when this ws:// URL addresses the in-app demo rather than a daemon. */
export function isDemoUrl(url: string | null | undefined): boolean {
  if (!url) return false;
  try {
    return new URL(url).hostname === DEMO_HOST;
  } catch {
    return false; // not a URL at all — certainly not the demo
  }
}
