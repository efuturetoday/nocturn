import { Injectable, signal, computed } from '@angular/core';
import type { ClientCommand, ServerEvent } from '../protocol/nocturn-protocol';

export type ConnectionState = 'disconnected' | 'connecting' | 'connected' | 'reconnecting';

/** A registered server-event listener; call the returned fn to unsubscribe. */
type EventListener = (event: ServerEvent) => void;

const BACKOFF_BASE_MS = 500;
const BACKOFF_CAP_MS = 30_000;

/**
 * ConnectionService owns THE one WebSocket to a nocturn daemon. It auto-reconnects with
 * exponential backoff + jitter and demuxes inbound `ServerEvent`s to registered listeners.
 *
 * It is deliberately decoupled from the domain services: WorkspaceService / ChatService
 * register via `onEvent()` and react to `state` — so ConnectionService depends on neither
 * (no circular DI). The control plane is STATE SYNC, not RPC: commands are fire-and-forget;
 * authoritative state arrives as pushed events. Consumers resync on (re)connect.
 */
@Injectable({ providedIn: 'root' })
export class ConnectionService {
  private ws: WebSocket | null = null;
  private url = '';
  private manualClose = false;
  private attempt = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly listeners = new Set<EventListener>();

  private readonly _state = signal<ConnectionState>('disconnected');
  /** The live connection state (drives UI + consumer resync effects). */
  readonly state = this._state.asReadonly();
  readonly connected = computed(() => this._state() === 'connected');

  private readonly _currentUrl = signal<string | null>(null);
  readonly currentUrl = this._currentUrl.asReadonly();

  /** Register a server-event listener. Returns an unsubscribe fn. */
  onEvent(fn: EventListener): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private token = '';

  /**
   * Connect to `ws://host:port/ws` with the pairing bearer. The bearer rides as `?token=` because
   * browsers/Capacitor can't set an Authorization header on the ws handshake (the daemon reads
   * either). Replaces any existing connection.
   */
  connect(url: string, token: string): void {
    this.disconnect();
    this.manualClose = false;
    this.url = url;
    this.token = token;
    this._currentUrl.set(url);
    this.attempt = 0;
    this.open();
  }

  /**
   * Force an immediate reconnect attempt if we have a target and aren't already connected/opening
   * — used when the app returns to the foreground (iOS suspends the socket in the background and
   * the backoff timer may be paused/long). No-op if never connected or already live.
   */
  reconnectNow(): void {
    if (!this.url || this.manualClose) return;
    if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) return;
    this.clearTimer();
    this.attempt = 0;
    this.open();
  }

  /** Close the socket and stop reconnecting. */
  disconnect(): void {
    this.manualClose = true;
    this.clearTimer();
    if (this.ws) {
      this.ws.onopen = this.ws.onclose = this.ws.onerror = this.ws.onmessage = null;
      try {
        this.ws.close();
      } catch {
        /* already closing */
      }
      this.ws = null;
    }
    this._state.set('disconnected');
  }

  /** Send a command. Fire-and-forget: dropped if not currently open (consumer resyncs). */
  send(cmd: ClientCommand): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(cmd));
    }
  }

  private open(): void {
    this._state.set(this.attempt === 0 ? 'connecting' : 'reconnecting');
    let ws: WebSocket;
    try {
      const u = new URL(this.url);
      if (this.token) u.searchParams.set('token', this.token);
      ws = new WebSocket(u.toString());
    } catch {
      this.scheduleReconnect();
      return;
    }
    this.ws = ws;

    ws.onopen = () => {
      this.attempt = 0;
      this._state.set('connected');
    };
    ws.onmessage = (ev) => this.dispatch(ev.data);
    ws.onerror = () => {
      /* the close handler drives reconnect */
    };
    ws.onclose = () => {
      this.ws = null;
      if (this.manualClose) return;
      this.scheduleReconnect();
    };
  }

  private dispatch(data: unknown): void {
    if (typeof data !== 'string') return;
    let event: ServerEvent;
    try {
      event = JSON.parse(data) as ServerEvent;
    } catch {
      return; // malformed frame — ignore, never throw
    }
    for (const fn of this.listeners) fn(event);
  }

  private scheduleReconnect(): void {
    this._state.set('reconnecting');
    const backoff = Math.min(BACKOFF_CAP_MS, BACKOFF_BASE_MS * 2 ** this.attempt);
    const jitter = Math.random() * backoff * 0.3;
    this.attempt++;
    this.clearTimer();
    this.reconnectTimer = setTimeout(() => this.open(), backoff + jitter);
  }

  private clearTimer(): void {
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }
}
