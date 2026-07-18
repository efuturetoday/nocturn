import { Component, ChangeDetectionStrategy, inject, signal, computed } from '@angular/core';
import { Router } from '@angular/router';
import { Capacitor } from '@capacitor/core';
import {
  IonHeader, IonToolbar, IonTitle, IonContent, IonList, IonItem, IonLabel, IonInput,
  IonButton, IonButtons, IonIcon, IonNote, IonListHeader, IonSpinner, IonText,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { wifiOutline, addCircleOutline, refreshOutline, radioOutline } from 'ionicons/icons';
import { DiscoveryService } from '../../core/services/discovery.service';
import { ConnectionService } from '../../core/services/connection.service';

@Component({
  selector: 'app-discover',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    IonHeader, IonToolbar, IonTitle, IonContent, IonList, IonItem, IonLabel, IonInput,
    IonButton, IonButtons, IonIcon, IonNote, IonListHeader, IonSpinner, IonText,
  ],
  styles: `
    /* Nebula star-field behind the Discover page only — opaque (over the bg colour) so page
       transitions stay correct. Fades into #0f071c at the bottom. */
    ion-content {
      --background:
        linear-gradient(
          to bottom,
          rgba(15, 7, 28, 0.55),
          rgba(15, 7, 28, 0.82) 55%,
          var(--ion-background-color) 100%
        ),
        url('/assets/brand/nebula.jpg') center top / cover no-repeat,
        var(--ion-background-color);
    }
    .hero {
      display: flex;
      flex-direction: column;
      align-items: center;
      text-align: center;
      padding: 12px 0 4px;
    }
    .hero img {
      filter: drop-shadow(0 12px 34px hsl(266 65% 45% / 0.55));
      animation: mascot-float 6s ease-in-out infinite;
    }
    .hero h1 {
      margin: 12px 0 2px;
      font-weight: 700;
      letter-spacing: 0.02em;
    }
    .hero p { margin: 0; color: var(--ion-color-medium); font-size: 0.9rem; }
    @keyframes mascot-float {
      0%, 100% { transform: translateY(0) rotate(-0.6deg); }
      50% { transform: translateY(-9px) rotate(0.6deg); }
    }
    @media (prefers-reduced-motion: reduce) {
      .hero img { animation: none; }
    }
  `,
  template: `
    <ion-header>
      <ion-toolbar>
        <ion-title>Connect to Nocturn</ion-title>
        @if (isNative) {
          <ion-buttons slot="end">
            <ion-button (click)="scan()" [disabled]="discovery.scanning()">
              <ion-icon slot="icon-only" name="refresh-outline" />
            </ion-button>
          </ion-buttons>
        }
      </ion-toolbar>
    </ion-header>

    <ion-content class="ion-padding">
      <div class="hero">
        <img src="/assets/brand/mascot.png" alt="Nocturn mascot" width="128" height="128" />
        <h1>Nocturn</h1>
        <p>Your secure personal assistant</p>
      </div>

      @if (connecting()) {
        <ion-item lines="none">
          <ion-spinner slot="start" />
          <ion-label>Connecting…</ion-label>
        </ion-item>
      }

      <!-- Manual entry — always available, the only path in the browser. -->
      <ion-list inset="true">
        <ion-list-header><ion-label>Enter host</ion-label></ion-list-header>
        <ion-item>
          <ion-input
            label="IP / host"
            labelPlacement="stacked"
            placeholder="192.168.1.20"
            [value]="host()"
            (ionInput)="host.set($any($event.target).value ?? '')"
          />
        </ion-item>
        <ion-item>
          <ion-input
            label="Port"
            labelPlacement="stacked"
            type="number"
            inputmode="numeric"
            [value]="port()"
            (ionInput)="port.set(+($any($event.target).value ?? 8765))"
          />
        </ion-item>
        <ion-item button detail="false" [disabled]="!host()" (click)="connect(manualUrl())">
          <ion-icon slot="start" name="add-circle-outline" />
          <ion-label>Connect</ion-label>
        </ion-item>
      </ion-list>

      @if (isNative) {
        <ion-list inset="true">
          <ion-list-header>
            <ion-label>Discovered</ion-label>
            @if (discovery.scanning()) { <ion-spinner slot="end" /> }
          </ion-list-header>
          @for (h of discovery.hosts(); track h.url) {
            <ion-item button detail="true" (click)="connect(h.url)">
              <ion-icon slot="start" name="radio-outline" />
              <ion-label>
                <h2>{{ h.name }}</h2>
                <ion-note>{{ h.url }}</ion-note>
              </ion-label>
            </ion-item>
          } @empty {
            @if (!discovery.scanning()) {
              <ion-item lines="none">
                <ion-label color="medium">
                  <ion-icon name="wifi-outline" /> No daemons found. Tap refresh or enter a host.
                </ion-label>
              </ion-item>
            }
          }
        </ion-list>
      } @else {
        <ion-text color="medium">
          <p class="ion-padding-horizontal">
            Running in the browser — mDNS is unavailable here. Enter the daemon's IP:port above.
          </p>
        </ion-text>
      }

      @if (discovery.savedHosts().length) {
        <ion-list inset="true">
          <ion-list-header><ion-label>Recent</ion-label></ion-list-header>
          @for (url of discovery.savedHosts(); track url) {
            <ion-item button detail="true" (click)="connect(url)">
              <ion-label>{{ url }}</ion-label>
            </ion-item>
          }
        </ion-list>
      }

      @if (discovery.error(); as err) {
        <ion-text color="warning"><p class="ion-padding-horizontal">{{ err }}</p></ion-text>
      }
    </ion-content>
  `,
})
export class DiscoverPage {
  protected readonly discovery = inject(DiscoveryService);
  protected readonly connection = inject(ConnectionService);
  private readonly router = inject(Router);

  protected readonly isNative = Capacitor.isNativePlatform();
  protected readonly host = signal(this.defaultHost());
  protected readonly port = signal(8765);
  protected readonly connecting = computed(
    () => this.connection.state() === 'connecting' || this.connection.state() === 'reconnecting',
  );

  constructor() {
    addIcons({ wifiOutline, addCircleOutline, refreshOutline, radioOutline });
    if (this.isNative) void this.discovery.scan();
  }

  protected scan(): void {
    void this.discovery.scan();
  }

  protected manualUrl(): string {
    return this.discovery.manualUrl(this.host(), this.port());
  }

  protected async connect(url: string): Promise<void> {
    this.connection.connect(url);
    await this.discovery.remember(url);
    await this.router.navigate(['/workspaces']);
  }

  private defaultHost(): string {
    return this.isNative ? '' : '127.0.0.1';
  }
}
