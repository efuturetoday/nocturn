import { Component, ChangeDetectionStrategy, inject, computed, signal, DestroyRef } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonContent,
  IonSpinner,
  IonFooter,
  IonSkeletonText,
  AlertController,
  ModalController,
} from '@ionic/angular/standalone';
import { PairPage } from '../pair/pair.page';
import { LucideRadio } from '@lucide/angular';
import { DiscoveryService } from '../../core/services/discovery.service';
import { DaemonService } from '../../core/services/daemon.service';
import { ConnectionService } from '../../core/services/connection.service';
import { AuthService } from '../../core/services/auth.service';
import { DEMO_BEARER, DEMO_HOST, isDemoUrl } from '../../core/demo/is-demo';

const RESCAN_MS = 4000;

@Component({
  selector: 'app-discover',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonContent, IonSpinner, IonFooter, IonSkeletonText, LucideRadio],
  template: `
    <ion-content [fullscreen]="true">
      <div class="nebula" aria-hidden="true"></div>
      <div class="page">
        <div class="hero">
          <img src="/assets/brand/mascot.png" alt="Nocturn mascot" width="200" height="200" />
          <h1>Nocturn</h1>
          <p>Your secure personal assistant</p>
        </div>

        @if (sameOrigin()) {
          <!-- Served by the daemon: there is exactly one host and we are already at it. -->
          @if (pairingCancelled()) {
            <!-- Cancelling used to leave a spinner with no control on it and no way back but a
                 reload — the whole screen is hidden in same-origin mode, so there was nothing else
                 to press. -->
            <div class="results">
              <button class="host" (click)="pairHere()">
                <svg lucideRadio [size]="21" />
                <span class="host-text"><b>Pair this browser</b><small>{{ daemonName() }}</small></span>
              </button>
            </div>
          } @else {
            <div class="searching"><ion-spinner name="crescent" /><span>Pairing this browser…</span></div>
          }
        } @else if (connecting()) {
          <div class="searching"><ion-spinner name="crescent" /><span>Connecting…</span></div>
        } @else {
          <div class="results">
            @for (h of discovery.hosts(); track h.url) {
              <button class="host" (click)="connect(h.url)">
                <svg lucideRadio [size]="21" />
                <span class="host-text"><b>{{ h.name }}</b><small>{{ h.url }}</small></span>
              </button>
            } @empty {
              @if (discovery.available) {
                <!-- Perpetual scan: skeleton placeholder while listening on the LAN. -->
                <div class="host skeleton">
                  <ion-skeleton-text [animated]="true" class="dot" />
                  <span class="host-text">
                    <ion-skeleton-text [animated]="true" style="width: 45%" />
                    <ion-skeleton-text [animated]="true" style="width: 75%" />
                  </span>
                </div>
              } @else {
                <!-- A browser cannot send a multicast query, so a skeleton here would be an
                     animation of something that is not happening — a wait with no end, which reads
                     as a broken app rather than as a missing capability. Say so, and offer the two
                     ways in that DO work here. -->
                <p class="no-scan">
                  A browser cannot look for servers on your network. Enter one below, or take a look
                  around first.
                </p>
              }
            }
          </div>
        }
      </div>
    </ion-content>

    @if (!sameOrigin()) {
      <ion-footer class="manual-footer">
        <button class="manual" (click)="manual()">Enter server manually</button>
        <!-- The demo existed all along and nothing pointed at it: the only way in was typing "demo"
             as the host in the manual dialog, which nobody guesses. It is the honest answer to
             "there is no server yet" — everything works, in-process, against a scripted daemon. -->
        <button class="manual demo" (click)="tryDemo()">Take a look around without a server</button>
      </ion-footer>
    }
  `,
  styles: `
    ion-content { --background: var(--ion-background-color); }
    .nebula {
      position: absolute; inset: 0; z-index: 0; pointer-events: none;
      background:
        linear-gradient(to bottom, rgba(15,7,28,0.5), rgba(15,7,28,0.8) 55%, var(--ion-background-color) 100%),
        url('/assets/brand/nebula.jpg') center top / cover no-repeat;
    }
    .page {
      position: relative; z-index: 1; min-height: 100%;
      display: flex; flex-direction: column; align-items: center;
      padding: 12vh 24px 24px; gap: 20px;
    }
    .hero { display: flex; flex-direction: column; align-items: center; }
    .hero img {
      filter: drop-shadow(0 14px 30px rgba(8, 4, 16, 0.7));
      animation: mascot-float 6s ease-in-out infinite;
    }
    .hero h1 { margin: 0.75rem 0 0; font-family: var(--font-display); font-weight: 600; letter-spacing: -0.01em; }
    .hero p { margin: 0.375rem 0 0; color: var(--ion-color-medium); font-size: 0.9rem; }
    @keyframes mascot-float {
      0%, 100% { transform: translateY(0) rotate(-0.6deg); }
      50% { transform: translateY(-9px) rotate(0.6deg); }
    }
    @media (prefers-reduced-motion: reduce) { .hero img { animation: none; } }

    .searching {
      display: flex; flex-direction: column; align-items: center; gap: 0.625rem;
      margin-top: 6vh;
      color: var(--ion-color-medium); font-size: 0.9rem;
    }
    .results { width: 100%; max-width: 26.25rem; display: flex; flex-direction: column; gap: 0.625rem; }
    .host {
      display: flex; align-items: center; gap: 0.75rem;
      width: 100%; padding: 0.875rem 1rem;
      background: var(--ion-background-color-step-100); color: var(--ion-text-color);
      border: 1px solid var(--ion-background-color-step-150); border-radius: 0.875rem;
      font: inherit; text-align: left; cursor: pointer;
    }
    .host > svg { color: var(--ion-color-primary); }
    .host-text { display: flex; flex-direction: column; }
    .host-text small { color: var(--ion-color-medium); font-size: 0.75rem; }
    .host.skeleton { cursor: default; }
    .host.skeleton .dot { width: 1.3rem; height: 1.3rem; border-radius: 50%; }
    .host.skeleton .host-text { flex: 1; gap: 0.375rem; }

    .no-scan {
      margin: 0; text-align: center;
      color: var(--ion-color-medium); font-size: 0.9rem; line-height: 1.5;
    }

    /* The home-indicator inset belongs to the footer, not to a button inside it. On the button it
       was the last child's bottom padding, so a second button pushed the inset into the middle of
       the stack and left the last one sitting on the indicator. */
    .manual-footer {
      --background: transparent; text-align: center;
      padding-bottom: var(--ion-safe-area-bottom, 0px);
    }
    .manual-footer::before { display: none; }
    .manual {
      background: none; border: none; cursor: pointer;
      color: var(--ion-color-primary); font: inherit; font-size: 0.9rem;
      text-decoration: underline;
      padding: 0.75rem;
      width: 100%;
    }
    /* The demo is the softer of the two offers: available, not the recommended path. */
    .manual.demo { color: var(--ion-color-medium); padding-top: 0; }
  `,
})
export class DiscoverPage {
  protected readonly discovery = inject(DiscoveryService);
  protected readonly connection = inject(ConnectionService);
  private readonly daemon = inject(DaemonService);
  private readonly auth = inject(AuthService);
  private readonly router = inject(Router);
  private readonly alerts = inject(AlertController);
  private readonly modalCtrl = inject(ModalController);

  protected readonly connecting = computed(
    () => this.connection.state() === 'connecting' || this.connection.state() === 'reconnecting',
  );

  /** True once the probe has found a daemon behind this page's own origin (browser builds only). */
  protected readonly sameOrigin = signal(false);

  /** The pairing sheet was dismissed without pairing, so offer a way back into it. */
  protected readonly pairingCancelled = signal(false);

  /** The daemon's own name, shown on the retry button so it is clear what is being paired with. */
  protected readonly daemonName = signal('');

  constructor() {
    const destroyRef = inject(DestroyRef);
    void this.daemon.probeLocal().then((info) => {
      if (info) {
        // Served by a daemon: there is nothing to discover and nowhere else to go. Skip straight to
        // pairing with the origin this page came from — scanning the LAN here would offer other
        // daemons this page cannot be repointed at.
        this.sameOrigin.set(true);
        this.daemonName.set(info.name);
        void this.pairHere();
        return;
      }
      // Perpetual discovery: scan now, then re-scan on an interval so the spinner keeps listening
      // and newly-appearing daemons show up. Cleaned up when the page is destroyed.
      //
      // Only where scanning is a thing that can happen. A browser has no way to browse a LAN, so a
      // timer there would wake every four seconds for the rest of the session to do nothing.
      if (!this.discovery.available) return;
      void this.discovery.scan();
      const timer = setInterval(() => void this.discovery.scan(), RESCAN_MS);
      destroyRef.onDestroy(() => clearInterval(timer));
    });
  }

  protected async manual(): Promise<void> {
    const alert = await this.alerts.create({
      header: 'Enter server',
      inputs: [
        { name: 'host', type: 'text', placeholder: 'IP / host (e.g. 192.168.1.20)' },
        // The daemon's own default (cmd/nocturn/cli.go, --port). This said 8765 while `serve` bound
        // 8080, so the one screen that exists for when discovery fails failed too, by default.
        { name: 'port', type: 'number', placeholder: 'Port', value: '8080' },
      ],
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        {
          text: 'Connect',
          handler: (v) => {
            const host = (v.host ?? '').trim();
            if (!host) return false;
            void this.connect(this.discovery.manualUrl(host, +(v.port || 8080)));
            return true;
          },
        },
      ],
    });
    await alert.present();
  }

  /** Enter the in-app demo: a scripted daemon behind the same wire protocol, no host involved. */
  protected async tryDemo(): Promise<void> {
    await this.connect(this.discovery.manualUrl(DEMO_HOST, 8765));
  }

  /** Pair with the daemon that served this page. Also the retry after a cancelled sheet. */
  protected async pairHere(): Promise<void> {
    this.pairingCancelled.set(false);
    await this.connect(this.daemon.localUrl());
  }

  protected async connect(url: string): Promise<void> {
    // A browser served by the daemon has one candidate host and cannot be repointed, so remembering
    // it would only leave a stale entry to mislead a later LAN session.
    // The demo is refused by remember() itself, so this stays about the one case it is about.
    if (!this.sameOrigin()) await this.discovery.remember(url);
    // The demo has nothing to pair with, so it skips the pairing sheet. Nor is it persisted: the
    // next launch starts here again, which is the point — it is a look around, not a destination.
    let bearer = isDemoUrl(url) ? DEMO_BEARER : await this.auth.bearerFor(url);
    if (!bearer) {
      // Ask the daemon which way in it has open before offering one. A code is armed only while
      // nothing in the household can relay a join code, so guessing gets it wrong exactly once per
      // household — and the wrong guess is a code field for a code that does not exist.
      const info = await this.daemon.probe(url);
      // Not paired to this daemon yet → pairing overlay; it dismisses with the bearer.
      const modal = await this.modalCtrl.create({
        component: PairPage,
        componentProps: { url, bootstrap: info?.bootstrap ?? true, paired: info?.paired ?? true },
        breakpoints: [0, 0.5, 0.9],
        initialBreakpoint: 0.5,
        handle: true,
      });
      await modal.present();
      const { data } = await modal.onDidDismiss<string>();
      if (!data) {
        // Cancelled. In same-origin mode the host list and the manual footer are both hidden, so
        // without this the page is left as a spinner with nothing on it to press.
        this.pairingCancelled.set(true);
        return;
      }
      bearer = data;
    }
    this.connection.connect(url, bearer);
    // Root nav: replace history so you can't swipe/back into the discover screen from the app.
    await this.router.navigate(['/app', 'home'], { replaceUrl: true });
  }
}
