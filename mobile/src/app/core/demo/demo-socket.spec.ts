import { describe, it, expect } from 'vitest';
import { ConnectionService } from '../services/connection.service';
import type { ServerEvent } from '../protocol/nocturn-protocol';

/**
 * The seam itself: ConnectionService must reach `connected` and demux the demo's events without
 * knowing it is not talking to a daemon. This is the one thing `demo-daemon.spec.ts` cannot show —
 * it tests the daemon in isolation, not that the app actually opens it.
 *
 * ConnectionService has no injected dependencies, so it is constructed directly rather than through
 * TestBed (which would drag in the Ionic bundle).
 */

/** Resolve once `check` holds, polling the signal — the socket opens on a real timer. */
function until(check: () => boolean, timeoutMs = 2000): Promise<void> {
  return new Promise((resolve, reject) => {
    const started = Date.now();
    const tick = (): void => {
      if (check()) return resolve();
      if (Date.now() - started > timeoutMs) return reject(new Error('timed out'));
      setTimeout(tick, 5);
    };
    tick();
  });
}

describe('ConnectionService against the demo host', () => {
  it('connects with no network and answers the commands the app sends on connect', async () => {
    const conn = new ConnectionService();
    const seen: ServerEvent[] = [];
    conn.onEvent((e) => seen.push(e));

    conn.connect('ws://demo:8765/ws', 'demo');
    await until(() => conn.connected());

    conn.send({ cmd: 'workspace.list' });
    await until(() => seen.some((e) => e.type === 'workspace.list'));

    conn.send({ cmd: 'chat.list', ws: 'main', kind: 'user' });
    await until(() => seen.some((e) => e.type === 'chat.list'));

    const chats = seen.find((e) => e.type === 'chat.list')!;
    expect(chats.chats.length).toBeGreaterThan(0);

    conn.disconnect();
    expect(conn.state()).toBe('disconnected');
  });

  it('stops the scripted turn when the connection is dropped mid-stream', async () => {
    const conn = new ConnectionService();
    const seen: ServerEvent[] = [];
    conn.onEvent((e) => seen.push(e));

    conn.connect('ws://demo:8765/ws', 'demo');
    await until(() => conn.connected());
    conn.send({ cmd: 'chat.submit', ws: 'main', kind: 'user', id: 'abcdef012345', text: 'hello' });
    await until(() => seen.some((e) => e.type === 'chat.turnStart'));

    conn.disconnect();
    const after = seen.length;
    await new Promise((r) => setTimeout(r, 300));

    // Every timer the turn scheduled belongs to the socket, so a disconnect really is the end of it.
    expect(seen.length).toBe(after);
  });
});
