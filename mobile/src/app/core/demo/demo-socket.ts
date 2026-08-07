/**
 * A WebSocket-shaped adapter in front of `DemoDaemon`, so `ConnectionService` cannot tell the demo
 * from a daemon. It implements only the surface that service actually touches — the same
 * ports-and-adapters trick the Go side uses for the terminal approver and the out-of-band broker.
 *
 * It owns every timer the scripted turn schedules, and `close()` clears all of them: a Disconnect
 * mid-turn must not leave a token arriving into a torn-down connection.
 */

import type { ClientCommand, ServerEvent } from '../protocol/nocturn-protocol';
import { DemoDaemon } from './demo-daemon';

/** The `WebSocket.readyState` values, spelled out so this file needs no DOM global. */
const CONNECTING = 0;
const OPEN = 1;
const CLOSED = 3;

/**
 * The part of a WebSocket `ConnectionService` uses — implemented by the real one and by the demo.
 * The handler shapes are the DOM's own, minus the `this` binding, so a real `WebSocket` satisfies
 * this without a cast.
 */
export interface SocketLike {
  readyState: number;
  onopen: ((ev: Event) => unknown) | null;
  onmessage: ((ev: MessageEvent) => unknown) | null;
  onerror: ((ev: Event) => unknown) | null;
  onclose: ((ev: CloseEvent) => unknown) | null;
  send(data: string): void;
  close(): void;
}

export class DemoSocket implements SocketLike {
  readyState = CONNECTING;
  onopen: ((ev: Event) => unknown) | null = null;
  onmessage: ((ev: MessageEvent) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;
  onclose: ((ev: CloseEvent) => unknown) | null = null;

  private readonly timers = new Set<ReturnType<typeof setTimeout>>();
  private readonly daemon: DemoDaemon;

  constructor() {
    this.daemon = new DemoDaemon({
      emit: (event) => this.deliver(event),
      schedule: (delayMs, fn) => this.defer(delayMs, fn),
      now: () => Date.now(),
    });
    // Open on the next tick, not synchronously: the caller is still assigning its handlers.
    this.defer(0, () => {
      this.readyState = OPEN;
      this.onopen?.(new Event('open'));
    });
  }

  send(data: string): void {
    if (this.readyState !== OPEN) return;
    let cmd: ClientCommand;
    try {
      cmd = JSON.parse(data) as ClientCommand;
    } catch {
      return; // malformed frame — ignore, never throw
    }
    this.daemon.handle(cmd);
  }

  close(): void {
    if (this.readyState === CLOSED) return;
    this.readyState = CLOSED;
    for (const t of this.timers) clearTimeout(t);
    this.timers.clear();
    this.onclose?.(new CloseEvent('close', { code: 1000 }));
  }

  private deliver(event: ServerEvent): void {
    if (this.readyState !== OPEN) return;
    this.onmessage?.(new MessageEvent('message', { data: JSON.stringify(event) }));
  }

  private defer(delayMs: number, fn: () => void): void {
    const t = setTimeout(() => {
      this.timers.delete(t);
      if (this.readyState !== CLOSED) fn();
    }, delayMs);
    this.timers.add(t);
  }
}
