import { Component, ChangeDetectionStrategy, inject, computed, DestroyRef } from '@angular/core';
import { Router } from '@angular/router';
import { IonContent, IonSpinner, IonIcon, IonFooter, IonSkeletonText, AlertController, ModalController } from '@ionic/angular/standalone';
import { PairPage } from '../pair/pair.page';
import { addIcons } from 'ionicons';
import { radioOutline } from 'ionicons/icons';
import { DiscoveryService } from '../../core/services/discovery.service';
import { ConnectionService } from '../../core/services/connection.service';
import { AuthService } from '../../core/services/auth.service';

const RESCAN_MS = 4000;

@Component({
  selector: 'app-discover',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonContent, IonSpinner, IonIcon, IonFooter, IonSkeletonText],
  template: `
    <ion-content [fullscreen]="true">
      <div class="nebula" aria-hidden="true"></div>
      <div class="page">
        <div class="hero">
          <img src="/assets/brand/mascot.png" alt="Nocturn mascot" width="200" height="200" />
          <h1>Nocturn</h1>
          <p>Your secure personal assistant</p>
        </div>

        @if (connecting()) {
          <div class="searching"><ion-spinner name="crescent" /><span>Connecting…</span></div>
        } @else {
          <div class="results">
            @for (h of discovery.hosts(); track h.url) {
              <button class="host" (click)="connect(h.url)">
                <ion-icon name="radio-outline" aria-hidden="true" />
                <span class="host-text"><b>{{ h.name }}</b><small>{{ h.url }}</small></span>
              </button>
            } @empty {
              <!-- Perpetual scan: skeleton placeholder while listening on the LAN. -->
              <div class="host skeleton">
                <ion-skeleton-text [animated]="true" class="dot" />
                <span class="host-text">
                  <ion-skeleton-text [animated]="true" style="width: 45%" />
                  <ion-skeleton-text [animated]="true" style="width: 75%" />
                </span>
              </div>
            }
          </div>
        }
      </div>
    </ion-content>

    <ion-footer class="manual-footer">
      <button class="manual" (click)="manual()">Enter server manually</button>
    </ion-footer>
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
    .host ion-icon { font-size: 1.3rem; color: var(--ion-color-primary); }
    .host-text { display: flex; flex-direction: column; }
    .host-text small { color: var(--ion-color-medium); font-size: 0.75rem; }
    .host.skeleton { cursor: default; }
    .host.skeleton .dot { width: 1.3rem; height: 1.3rem; border-radius: 50%; }
    .host.skeleton .host-text { flex: 1; gap: 0.375rem; }

    .manual-footer { --background: transparent; text-align: center; }
    .manual-footer::before { display: none; }
    .manual {
      background: none; border: none; cursor: pointer;
      color: var(--ion-color-primary); font: inherit; font-size: 0.9rem;
      text-decoration: underline;
      padding: 0.75rem; padding-bottom: calc(0.75rem + var(--ion-safe-area-bottom, 0px));
      width: 100%;
    }
  `,
})
export class DiscoverPage {
  protected readonly discovery = inject(DiscoveryService);
  protected readonly connection = inject(ConnectionService);
  private readonly auth = inject(AuthService);
  private readonly router = inject(Router);
  private readonly alerts = inject(AlertController);
  private readonly modalCtrl = inject(ModalController);

  protected readonly connecting = computed(
    () => this.connection.state() === 'connecting' || this.connection.state() === 'reconnecting',
  );

  constructor() {
    addIcons({ radioOutline });
    // Perpetual discovery: scan now, then re-scan on an interval so the spinner keeps listening
    // and newly-appearing daemons show up. Cleaned up when the page is destroyed.
    void this.discovery.scan();
    const timer = setInterval(() => void this.discovery.scan(), RESCAN_MS);
    inject(DestroyRef).onDestroy(() => clearInterval(timer));
  }

  protected async manual(): Promise<void> {
    const alert = await this.alerts.create({
      header: 'Enter server',
      inputs: [
        { name: 'host', type: 'text', placeholder: 'IP / host (e.g. 192.168.1.20)' },
        { name: 'port', type: 'number', placeholder: 'Port', value: '8765' },
      ],
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        {
          text: 'Connect',
          handler: (v) => {
            const host = (v.host ?? '').trim();
            if (!host) return false;
            void this.connect(this.discovery.manualUrl(host, +(v.port || 8765)));
            return true;
          },
        },
      ],
    });
    await alert.present();
  }

  protected async connect(url: string): Promise<void> {
    await this.discovery.remember(url);
    let bearer = await this.auth.bearerFor(url);
    if (!bearer) {
      // Not paired to this daemon yet → pairing overlay; it dismisses with the bearer.
      const modal = await this.modalCtrl.create({
        component: PairPage,
        componentProps: { url },
        breakpoints: [0, 0.5, 0.9],
        initialBreakpoint: 0.5,
        handle: true,
      });
      await modal.present();
      const { data } = await modal.onDidDismiss<string>();
      if (!data) return; // cancelled
      bearer = data;
    }
    this.connection.connect(url, bearer);
    // Root nav: replace history so you can't swipe/back into the discover screen from the app.
    await this.router.navigate(['/tabs', 'chat'], { replaceUrl: true });
  }
}
