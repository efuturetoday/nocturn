import { Injectable, inject, signal, effect } from '@angular/core';
import { Router } from '@angular/router';
import { Preferences } from '@capacitor/preferences';
import { Device } from '@capacitor/device';
import { Capacitor } from '@capacitor/core';
import { ConnectionService } from './connection.service';
import { DEMO_BEARER, isDemoUrl } from '../demo/is-demo';
import { httpBase } from '../util/daemon-url';
import type {
  PairResponse,
  JoinResponse,
  JoinConfirmResponse,
  PendingJoin,
  EnrolledDevice,
} from '../protocol/nocturn-protocol';

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

  private readonly _devices = signal<EnrolledDevice[]>([]);
  /** The household's enrolled devices (live from the `device.list` event). */
  readonly devices = this._devices.asReadonly();

  private readonly _selfId = signal<string | null>(null);
  /** Which of those devices is this one — the daemon says so, rather than the client matching names. */
  readonly selfId = this._selfId.asReadonly();

  constructor() {
    this.conn.onEvent((e) => {
      if (e.type === 'join.list') this._joins.set(e.joins);
      if (e.type === 'device.list') {
        this._devices.set(e.devices);
        if (e.self) this._selfId.set(e.self);
      }
    });
    // Re-request pending joins and the device roster on every (re)connect; then they arrive live.
    effect(() => {
      if (this.conn.state() === 'connected') {
        this.conn.send({ cmd: 'join.list' });
        this.conn.send({ cmd: 'device.list' });
      }
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

  /** The stored bearer for a daemon (keyed by host), or null if this device isn't paired to it. The
      demo has no daemon to pair with, so it answers with its stand-in and never touches storage. */
  async bearerFor(wsUrl: string): Promise<string | null> {
    if (isDemoUrl(wsUrl)) return DEMO_BEARER;
    const { value } = await Preferences.get({ key: this.key(wsUrl) });
    return value ?? null;
  }

  /** Register (or, with "", clear) this device's native push token so the daemon can wake it. */
  async registerPush(wsUrl: string, token: string): Promise<void> {
    if (isDemoUrl(wsUrl)) return; // no host to POST to — the demo is entirely in-process
    const bearer = await this.bearerFor(wsUrl);
    if (!bearer) return;
    await fetch(httpBase(wsUrl) + '/register', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${bearer}` },
      body: JSON.stringify({ token, platform: this.platform() }),
    });
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

  /**
   * Ask an already-paired daemon to join → the joinId, plus how many paired devices are connected to
   * display the code.
   *
   * `reachable` is not decoration. The join list is gated on the enrol capability, so a household
   * whose only such device is asleep has nowhere to show the code — and a caller told nothing but
   * "here is your joinId" waits on a code field forever, with no error to explain it.
   */
  async join(wsUrl: string): Promise<{ joinId: string; reachable: number }> {
    const res = await this.post<JoinResponse>(wsUrl, '/join', {
      name: await this.deviceName(),
      platform: this.platform(),
    });
    return { joinId: res.joinId, reachable: res.reachable ?? 0 };
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

  /**
   * Revoke a device's bearer on the daemon.
   *
   * Forgetting THIS device is allowed and is the "sign this browser out" action — the daemon closes
   * the socket with 4401 on the next connection, which the existing auth-error effect already turns
   * into "drop the stored bearer and go back to pairing". So the sign-out path is the same one a
   * revoked-elsewhere device takes, and there is only one of them to get right.
   */
  forget(id: string): void {
    this.conn.send({ cmd: 'device.forget', id });
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
      this.cachedName = label || this.fallbackName();
    } catch {
      this.cachedName = this.fallbackName();
    }
    return this.cachedName;
  }

  /** The label when the platform will not name itself. It is the name a human reads in the device
      list, so a browser must not appear there as a phone. */
  private fallbackName(): string {
    return this.platform() === 'web' ? 'Nocturn Web' : 'Nocturn Mobile';
  }

  // ── internals ────────────────────────────────────────────────────────────────

  private async post<T>(wsUrl: string, path: string, body: unknown): Promise<T> {
    const r = await fetch(httpBase(wsUrl) + path, {
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

  private key(wsUrl: string): string {
    return 'nocturn.bearer.' + new URL(wsUrl).host;
  }
}
