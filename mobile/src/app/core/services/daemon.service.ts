import { Injectable, signal } from '@angular/core';
import { httpBase, sameOriginWsUrl, WS_PATH } from '../util/daemon-url';
import { isDemoUrl } from '../demo/is-demo';

/** What a daemon says about itself before anyone has a bearer (`GET /daemon.json`). */
export interface DaemonInfo {
  name: string;
  version: string;
  /** The WebSocket path, so the client does not hardcode `/ws`. */
  ws: string;
  /**
   * Something in the household can already bring another device in, so the join flow has someone on
   * the other end of it.
   *
   * This is the bit that stops the sheet guessing. `bootstrap` alone could not tell "join is the way
   * in" from "there is no way in right now", and offering the join flow in the second case walks the
   * user into asking for a code that no device in existence can display.
   */
  paired: boolean;
  /** A pairing code is armed right now — offer the code field. */
  bootstrap: boolean;
}

/** How long to wait for a descriptor before treating the host as not-a-nocturn. */
const PROBE_TIMEOUT_MS = 2500;

/**
 * DaemonService answers two questions no other service can.
 *
 * **"Was this page served by a nocturn?"** The native app always knows where it is pointed — mDNS
 * found it or a human typed it. A browser knows only its own location, and the answer decides
 * whether it shows a host picker at all or simply connects back to where it came from. That is the
 * whole of "same-origin mode": no discovery screen, no manual host entry, no mDNS.
 *
 * **"Which way in should the pairing sheet offer?"** A daemon with a bootstrap code armed wants that
 * code; one that is already a household wants a join code relayed from an existing device. Asking
 * beats guessing, and it costs one unauthenticated GET.
 *
 * A daemon too old to answer, or a host that is not a daemon at all, simply reports null — every
 * caller then behaves exactly as it did before this existed.
 */
@Injectable({ providedIn: 'root' })
export class DaemonService {
  private readonly _local = signal<DaemonInfo | null>(null);
  /** The daemon that served this page, or null once probed and found absent. */
  readonly local = this._local.asReadonly();

  private localProbe: Promise<DaemonInfo | null> | null = null;

  /**
   * True once `probeLocal` has found a daemon behind this page's own origin. Read it only after
   * awaiting that probe — before it, "not yet asked" and "no" are the same value.
   */
  get sameOrigin(): boolean {
    return this._local() !== null;
  }

  /** The WebSocket URL of the daemon that served this page. Meaningless unless `sameOrigin`. */
  localUrl(): string {
    return sameOriginWsUrl(this._local()?.ws || WS_PATH);
  }

  /**
   * Ask this page's own origin whether a daemon is behind it. A daemon that answers is remembered for
   * the app's lifetime — that answer is a property of how the app was loaded and cannot change
   * without a reload.
   *
   * A failure is NOT remembered. `fetchInfo` reports every failure the same way, as null, so a page
   * opened one second before its daemon finished binding is indistinguishable from a page nobody
   * served — and caching that null would disable the same-origin path for as long as the app stayed
   * open, with a reload the only way back. The cache holds the fact, not the absence of one.
   */
  probeLocal(): Promise<DaemonInfo | null> {
    this.localProbe ??= this.fetchInfo(location.origin).then((info) => {
      this._local.set(info);
      if (!info) this.localProbe = null;
      return info;
    });
    return this.localProbe;
  }

  /**
   * Ask a specific daemon about itself. Used before pairing, so the sheet knows which way in to
   * offer. Not cached: `bootstrap` is a live fact that a successful pairing changes.
   */
  probe(wsUrl: string): Promise<DaemonInfo | null> {
    if (isDemoUrl(wsUrl)) return Promise.resolve(null); // no host to ask — the demo is in-process
    let base: string;
    try {
      base = httpBase(wsUrl);
    } catch {
      return Promise.resolve(null);
    }
    return this.fetchInfo(base);
  }

  /**
   * GET <base>/daemon.json, or null for anything that is not a well-formed descriptor.
   *
   * Every failure is the same answer on purpose. A 404 (a daemon predating this endpoint), a
   * connection refused (nothing there), a timeout (something there that is not answering) and a body
   * that is not the expected shape all mean "do not take the same-origin path", and distinguishing
   * them would only give callers four ways to write the same fallback.
   */
  private async fetchInfo(base: string): Promise<DaemonInfo | null> {
    try {
      const res = await fetch(`${base}/daemon.json`, {
        signal: AbortSignal.timeout(PROBE_TIMEOUT_MS),
        cache: 'no-store',
      });
      if (!res.ok) return null;
      const body = (await res.json()) as Partial<DaemonInfo>;
      if (typeof body?.ws !== 'string' || typeof body?.bootstrap !== 'boolean') return null;
      return {
        name: body.name ?? 'nocturn',
        version: body.version ?? '',
        ws: body.ws,
        // A daemon predating this field is one that always armed a code at startup and never
        // re-armed. Reading its silence as "paired" is the safe half: the sheet then offers the join
        // flow and the code field side by side rather than auto-walking into either.
        paired: body.paired ?? true,
        bootstrap: body.bootstrap,
      };
    } catch {
      return null;
    }
  }
}
