import { Injectable, inject, signal, effect } from '@angular/core';
import { Router } from '@angular/router';
import { Preferences } from '@capacitor/preferences';
import { Device } from '@capacitor/device';
import { Capacitor } from '@capacitor/core';
import { ConnectionService } from './connection.service';
import type { PairResponse, JoinResponse, JoinConfirmResponse, PendingJoin } from '../protocol/nocturn-protocol';

/**
 * AuthService owns device pairing + the per-daemon bearer. A device must pair before `/ws` will
 * accept it (else the daemon closes with 4401). Pairing is HTTP (not the WebSocket): redeem a
 * bootstrap OTP/QR-secret (`/pair`), or join an already-paired daemon by relaying a code (`/join` →
 * `/join/confirm`). The bearer is stored per daemon host (Preferences today; secure storage is a
 * later hardening) and sent on the ws URL as `?token=` (browsers/Capacitor can't set headers on the
 * ws handshake).
 *
 * Pending device-joins (the codes an admin device relays) arrive as the `join.list` WS event —
 * re-requested via `join.list` on (re)connect.
 */
@Injectable({ providedIn: 'root' })
export class AuthService {
  private readonly conn = inject(ConnectionService);
  private readonly router = inject(Router);

  private readonly _joins = signal<PendingJoin[]>([]);
  /** Pending device-join requests + their codes (live from the `join.list` event). */
  readonly joins = this._joins.asReadonly();

  constructor() {
    this.conn.onEvent((e) => {
      if (e.type === 'join.list') this._joins.set(e.joins);
    });
    // Re-request pending joins on every (re)connect; then they arrive live.
    effect(() => {
      if (this.conn.state() === 'connected') this.conn.send({ cmd: 'join.list' });
    });

    // Bearer rejected (close 4401) → forget it and send the user back to pair.
    effect(() => {
      const url = this.conn.authError();
      if (!url) return;
      void this.clear(url);
      this.conn.clearAuthError();
      void this.router.navigate(['/discover'], { replaceUrl: true });
    });
  }

  /** The stored bearer for a daemon (keyed by host), or null if this device isn't paired to it. */
  async bearerFor(wsUrl: string): Promise<string | null> {
    const { value } = await Preferences.get({ key: this.key(wsUrl) });
    return value ?? null;
  }

  /** Redeem the bootstrap OTP or QR secret → bearer (first device). */
  async pair(wsUrl: string, credential: string): Promise<string> {
    const res = await this.post<PairResponse>(wsUrl, '/pair', {
      credential: credential.trim(),
      name: await this.deviceName(),
      platform: this.platform(),
    });
    await this.store(wsUrl, res.bearer);
    return res.bearer;
  }

  /** Ask an already-paired daemon to join → joinId (the code is revealed on a paired device). */
  async join(wsUrl: string): Promise<string> {
    const res = await this.post<JoinResponse>(wsUrl, '/join', {
      name: await this.deviceName(),
      platform: this.platform(),
    });
    return res.joinId;
  }

  /** Confirm a join with the code read off a paired device → bearer. */
  async joinConfirm(wsUrl: string, joinId: string, code: string): Promise<string> {
    const res = await this.post<JoinConfirmResponse>(wsUrl, '/join/confirm', { joinId, code: code.trim() });
    await this.store(wsUrl, res.bearer);
    return res.bearer;
  }

  /** Forget this daemon's bearer (e.g. after a 4401 or an explicit unpair). */
  async clear(wsUrl: string): Promise<void> {
    await Preferences.remove({ key: this.key(wsUrl) });
  }

  /** Always send the platform (ios | android | web) — the daemon records it for push routing. */
  private platform(): string {
    return Capacitor.getPlatform();
  }

  /** A permission-free device label (from @capacitor/device). */
  private cachedName: string | null = null;
  async deviceName(): Promise<string> {
    if (this.cachedName) return this.cachedName;
    try {
      const info = await Device.getInfo();
      const label = info.name && info.name !== info.model ? info.name : `${info.manufacturer} ${info.model}`.trim();
      this.cachedName = label || 'Nocturn Mobile';
    } catch {
      this.cachedName = 'Nocturn Mobile';
    }
    return this.cachedName;
  }

  // ── internals ────────────────────────────────────────────────────────────────

  private async post<T>(wsUrl: string, path: string, body: unknown): Promise<T> {
    const r = await fetch(this.httpBase(wsUrl) + path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!r.ok) throw new Error((await r.text()) || `HTTP ${r.status}`);
    return (await r.json()) as T;
  }

  private async store(wsUrl: string, bearer: string): Promise<void> {
    await Preferences.set({ key: this.key(wsUrl), value: bearer });
  }

  /** ws://host:port/ws → http(s)://host:port (pairing is plain HTTP on the same host). */
  private httpBase(wsUrl: string): string {
    const u = new URL(wsUrl);
    return `${u.protocol === 'wss:' ? 'https:' : 'http:'}//${u.host}`;
  }

  private key(wsUrl: string): string {
    return 'nocturn.bearer.' + new URL(wsUrl).host;
  }
}
