import { Injectable, inject, signal, effect } from '@angular/core';
import { Preferences } from '@capacitor/preferences';
import { Device } from '@capacitor/device';
import { Capacitor } from '@capacitor/core';
import { ConnectionService } from './connection.service';
import type { PairResponse, JoinResponse, JoinConfirmResponse, PendingJoin, DeviceMeta } from '../protocol/nocturn-protocol';

/**
 * AuthService owns device pairing + the per-daemon bearer. A device must pair before `/ws` will
 * accept it (else HTTP 401). Pairing is HTTP (not the WebSocket): redeem a bootstrap OTP/QR-secret
 * (`/pair`), or join an already-paired daemon by relaying a code (`/join` → `/join/confirm`). The
 * bearer is stored per daemon host (Preferences today; Keychain/secure-storage is a later hardening)
 * and sent on the ws URL as `?token=` (browsers/Capacitor can't set headers on the ws handshake).
 *
 * Pending device-joins (the codes an admin device relays) arrive as the `joins` WS event — kept in
 * the `joins` signal, re-requested via `listJoins` on (re)connect.
 */
@Injectable({ providedIn: 'root' })
export class AuthService {
  private readonly conn = inject(ConnectionService);

  private readonly _joins = signal<PendingJoin[]>([]);
  /** Pending device-join requests + their codes (live from the `joins` event). */
  readonly joins = this._joins.asReadonly();

  private readonly _devices = signal<DeviceMeta[]>([]);
  /** The paired devices (live from the `devices` event). */
  readonly devices = this._devices.asReadonly();

  constructor() {
    this.conn.onEvent((e) => {
      if (e.type === 'joins') this._joins.set(e.items);
      else if (e.type === 'devices') this._devices.set(e.items);
    });
    // Re-request joins + devices on every (re)connect; then they arrive live.
    effect(() => {
      if (this.conn.state() !== 'connected') return;
      this.conn.send({ cmd: 'listJoins' });
      this.conn.send({ cmd: 'listDevices' });
    });
  }

  /** Unpair a device by its public handle. Its bearer stops working on next connect. */
  revokeDevice(id: string): void {
    this.conn.send({ cmd: 'revokeDevice', id });
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

  /** Always send the platform (ios | android | web) — the daemon records it for push routing. */
  private platform(): string {
    return Capacitor.getPlatform();
  }

  /** A permission-free device label for the paired-devices list (from @capacitor/device). */
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

  /** Confirm a join with the code read off a paired device → bearer. */
  async joinConfirm(wsUrl: string, joinId: string, code: string): Promise<string> {
    const res = await this.post<JoinConfirmResponse>(wsUrl, '/join/confirm', { joinId, code: code.trim() });
    await this.store(wsUrl, res.bearer);
    return res.bearer;
  }

  /** Forget this daemon's bearer (e.g. after a 401 or an explicit unpair). */
  async clear(wsUrl: string): Promise<void> {
    await Preferences.remove({ key: this.key(wsUrl) });
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
