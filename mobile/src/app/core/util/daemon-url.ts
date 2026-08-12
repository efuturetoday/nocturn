/**
 * The two directions between a daemon's WebSocket URL and its HTTP origin.
 *
 * The app addresses a daemon by ONE thing: the `ws://host:port/ws` URL it discovered or was given.
 * Pairing, push registration and the daemon descriptor are plain HTTP on that same host, so every
 * one of them needs the same conversion, and doing it in three places is how two of them end up
 * disagreeing about `wss:`.
 */

/** The daemon's WebSocket path. The daemon reports it in `daemon.json`; this is the fallback. */
export const WS_PATH = '/ws';

/** `ws://host:port/ws` → `http://host:port` (and `wss:` → `https:`). */
export function httpBase(wsUrl: string): string {
  const u = new URL(wsUrl);
  return `${u.protocol === 'wss:' ? 'https:' : 'http:'}//${u.host}`;
}

/**
 * The WebSocket URL of the daemon that served this page.
 *
 * Only meaningful in a browser the daemon itself is serving, which is the whole point: a page has no
 * way to be told where its daemon is, but it does not need one — it came from there. Deriving the
 * scheme from `location` rather than hardcoding `ws:` is what keeps this correct the day the daemon
 * is put behind TLS, where a `ws:` socket from an `https:` page is blocked as mixed content.
 */
export function sameOriginWsUrl(path = WS_PATH): string {
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${scheme}//${location.host}${path}`;
}
