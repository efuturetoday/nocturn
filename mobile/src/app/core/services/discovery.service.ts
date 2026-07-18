import { Injectable, signal } from '@angular/core';
import { Preferences } from '@capacitor/preferences';
import { mDNS, type MdnsService } from '@devioarts/capacitor-mdns';

/** Nocturn's Bonjour service type (trailing dot required by the plugin). */
const SERVICE_TYPE = '_nocturn._tcp.';
const DEFAULT_PATH = '/ws';
const DISCOVER_TIMEOUT_MS = 4000;

const KEY_LAST = 'nocturn.lastHost';
const KEY_SAVED = 'nocturn.savedHosts';

/** A connectable daemon: a display name + the full ws:// URL to open. */
export interface DiscoveredHost {
  name: string;
  url: string;
}

/**
 * DiscoveryService finds nocturn daemons on the LAN via mDNS (`_nocturn._tcp`, TXT `path=/ws`)
 * and falls back to manual host:port entry (mandatory — the daemon defaults to a loopback bind
 * and iOS may deny the local-network permission). Chosen/typed hosts persist via Preferences.
 */
@Injectable({ providedIn: 'root' })
export class DiscoveryService {
  private readonly _hosts = signal<DiscoveredHost[]>([]);
  readonly hosts = this._hosts.asReadonly();

  private readonly _scanning = signal(false);
  readonly scanning = this._scanning.asReadonly();

  private readonly _lastHost = signal<string | null>(null);
  readonly lastHost = this._lastHost.asReadonly();

  private readonly _savedHosts = signal<string[]>([]);
  readonly savedHosts = this._savedHosts.asReadonly();

  private readonly _error = signal<string | null>(null);
  readonly error = this._error.asReadonly();

  constructor() {
    void this.loadPersisted();
  }

  /** Browse the LAN for nocturn daemons. Populates `hosts`; sets `error` on failure. */
  async scan(): Promise<void> {
    this._scanning.set(true);
    this._error.set(null);
    try {
      const res = await mDNS.discover({ type: SERVICE_TYPE, timeout: DISCOVER_TIMEOUT_MS });
      this._hosts.set(res.services.map((s) => this.toHost(s)).filter((h): h is DiscoveredHost => h !== null));
      if (res.error && res.errorMessage) this._error.set(res.errorMessage);
    } catch (e) {
      // Web platform / permission denied / no plugin: fall back to manual entry.
      this._error.set(e instanceof Error ? e.message : 'mDNS discovery unavailable');
      this._hosts.set([]);
    } finally {
      this._scanning.set(false);
    }
  }

  /** Build a ws:// URL from a manual host + port (no scheme/path needed from the user). */
  manualUrl(host: string, port: number): string {
    const h = host.trim().replace(/^wss?:\/\//, '').replace(/\/.*$/, '');
    return `ws://${h}:${port}${DEFAULT_PATH}`;
  }

  /** Persist a URL as last-used and add it to the saved list. */
  async remember(url: string): Promise<void> {
    this._lastHost.set(url);
    await Preferences.set({ key: KEY_LAST, value: url });
    const saved = this._savedHosts();
    if (!saved.includes(url)) {
      const next = [url, ...saved].slice(0, 10);
      this._savedHosts.set(next);
      await Preferences.set({ key: KEY_SAVED, value: JSON.stringify(next) });
    }
  }

  private toHost(s: MdnsService): DiscoveredHost | null {
    const ip = s.hosts.find((h) => !h.includes(':')) ?? s.hosts[0]; // prefer IPv4
    if (!ip) return null;
    const path = s.txt?.['path'] ?? DEFAULT_PATH;
    return { name: s.name, url: `ws://${ip}:${s.port}${path}` };
  }

  /** The persisted last-used ws:// URL (read straight from storage; survives app reload). */
  async lastHostValue(): Promise<string | null> {
    const { value } = await Preferences.get({ key: KEY_LAST });
    return value ?? null;
  }

  private async loadPersisted(): Promise<void> {
    const [{ value: last }, { value: saved }] = await Promise.all([
      Preferences.get({ key: KEY_LAST }),
      Preferences.get({ key: KEY_SAVED }),
    ]);
    if (last) this._lastHost.set(last);
    if (saved) {
      try {
        this._savedHosts.set(JSON.parse(saved) as string[]);
      } catch {
        /* ignore corrupt cache */
      }
    }
  }
}
