import { Component, ChangeDetectionStrategy, computed, inject } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote, IonChip, IonSpinner,
  IonButton, AlertController,
} from "@ionic/angular/standalone";
import { Capacitor } from '@capacitor/core';
import { LucideLogOut } from '@lucide/angular';
import { ConnectionService } from '../../core/services/connection.service';
import { AuthService } from '../../core/services/auth.service';
import { AccountsService } from '../../core/services/accounts.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import { isDemoUrl } from '../../core/demo/is-demo';
import type { EnrolledDevice } from '../../core/protocol/nocturn-protocol';

@Component({
  selector: 'app-settings',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    WorkspaceHeaderComponent, LucideLogOut, IonContent, IonList, IonListHeader, IonItem, IonLabel,
    IonNote, IonChip, IonSpinner, IonButton,
  ],
  template: `
    <app-workspace-header />

    <ion-content>
      <ion-list inset="true">
        <ion-list-header><ion-label>Pairing requests</ion-label></ion-list-header>
        @for (j of auth.joins(); track j.joinId) {
          <ion-item>
            <ion-label>
              <h2>{{ j.name }}</h2>
              <ion-note>Share this code with the new device</ion-note>
            </ion-label>
            <ion-chip slot="end" color="primary">{{ j.code }}</ion-chip>
          </ion-item>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No pending requests.</ion-label></ion-item>
        }
      </ion-list>

      <!--
        The exit from "my phone is lost". Until this existed a bearer was valid until someone edited
        devices.json by hand and restarted the daemon — a remedy nobody finds at the moment they need
        it, and one that needs shell access to a machine they may be nowhere near.
      -->
      <ion-list inset="true">
        <ion-list-header><ion-label>Devices</ion-label></ion-list-header>
        @for (d of auth.devices(); track d.id) {
          <ion-item>
            <ion-label>
              <h2>{{ d.name }}</h2>
              <ion-note>{{ deviceSubtitle(d) }}</ion-note>
            </ion-label>
            @if (d.id === auth.selfId()) {
              <ion-chip slot="end" color="medium">This device</ion-chip>
            }
            <ion-button slot="end" fill="clear" color="danger" (click)="forget(d)">Forget</ion-button>
          </ion-item>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No devices.</ion-label></ion-item>
        }
      </ion-list>

      <!--
        Connecting an MCP account needs the OAuth redirect to come back to us, and the daemon's
        redirect is the custom scheme nocturn://oauth/callback — the OS routes that to the installed
        app, and a browser tab can never receive it. Showing a Connect button that cannot complete is
        worse than not showing one, so a browser is told where the flow does work.
      -->
      <ion-list inset="true">
        <ion-list-header><ion-label>Accounts</ion-label></ion-list-header>
        @if (webBuild) {
          <ion-item lines="none">
            <ion-label class="ion-text-wrap" color="medium">
              Connect accounts from the companion app, or run
              <code>nocturn auth &lt;provider&gt;</code> on the daemon.
            </ion-label>
          </ion-item>
        } @else {
        @for (a of accounts.accounts(); track a.server) {
          <ion-item [button]="!a.connected" [disabled]="accounts.busy()" (click)="a.connected || connect(a.server)">
            <ion-label>
              <h2>{{ a.server }}</h2>
              <ion-note>MCP account</ion-note>
            </ion-label>
            @if (a.connected) {
              <ion-chip slot="end" color="success">Connected</ion-chip>
            } @else if (accounts.connecting() === a.server) {
              <ion-spinner slot="end" name="crescent" />
            } @else {
              <ion-note slot="end" color="primary">Connect</ion-note>
            }
          </ion-item>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No connectable accounts.</ion-label></ion-item>
        }
        }
      </ion-list>

      <ion-list inset="true">
        <ion-list-header><ion-label>Connection</ion-label></ion-list-header>
        @if (demo()) {
          <ion-item>
            <ion-label>
              <h2>Demo mode</h2>
              <ion-note>Sample data. No daemon is connected and nothing leaves this device.</ion-note>
            </ion-label>
          </ion-item>
        }
        <ion-item>
          <ion-label>
            <h2>{{ connection.currentUrl() ?? '—' }}</h2>
            <ion-note>daemon</ion-note>
          </ion-label>
          <ion-chip slot="end" [color]="connection.connected() ? 'success' : 'warning'">
            {{ connection.state() }}
          </ion-chip>
        </ion-item>
        <ion-item button lines="none" (click)="disconnect()">
          <svg lucideLogOut slot="start" [size]="21" class="danger" />
          <ion-label color="danger">Disconnect</ion-label>
        </ion-item>
      </ion-list>
    </ion-content>
  `,
  styles: `
    .danger { color: var(--ion-color-danger); }
  `,
})
export class SettingsPage {
  protected readonly connection = inject(ConnectionService);
  protected readonly auth = inject(AuthService);
  protected readonly accounts = inject(AccountsService);
  private readonly router = inject(Router);
  private readonly alerts = inject(AlertController);

  /** Say so when the app is running against the in-app demo rather than a daemon. */
  protected readonly demo = computed(() => isDemoUrl(this.connection.currentUrl()));

  /**
   * True in a browser, false in the native app. Not a signal: how the app was loaded is fixed for
   * the lifetime of the page. It gates the one screen where the two genuinely differ — an OAuth
   * redirect to a custom scheme reaches an installed app and nothing else.
   */
  protected readonly webBuild = Capacitor.getPlatform() === 'web';

  protected connect(server: string): void {
    this.accounts.connect(server);
  }

  /** What a device is, in one line: its class and when it last connected. */
  protected deviceSubtitle(d: EnrolledDevice): string {
    const kind = { app: 'phone', web: 'browser', appliance: 'appliance', tool: 'command line' }[d.class ?? ''] ?? 'device';
    if (!d.lastUsed) return `${kind} · never connected`;
    return `${kind} · last seen ${new Date(d.lastUsed).toLocaleDateString()}`;
  }

  /**
   * Revoke a device, behind a confirmation.
   *
   * Irreversible in the only sense that matters — the bearer cannot be handed back, the device has to
   * pair again — so it is worth one tap. Forgetting THIS device is allowed and is how you sign a
   * browser out; the wording changes because the consequence does.
   */
  protected async forget(d: EnrolledDevice): Promise<void> {
    const self = d.id === this.auth.selfId();
    const alert = await this.alerts.create({
      header: self ? 'Sign out this device?' : `Forget ${d.name}?`,
      message: self
        ? 'This device will be signed out and will have to pair again.'
        : d.class === 'tool'
          // The command line is the one row that heals: the daemon writes it a fresh credential
          // straight away, so this rotates rather than revokes. Which is what you want from it — the
          // reason to do this is that the file leaked, and the copy that leaked stops working.
          ? 'The command line will be issued a new credential. Any copy of the old one stops working.'
          : `${d.name} will lose access immediately and will have to pair again.`,
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        { text: self ? 'Sign out' : 'Forget', role: 'destructive', handler: () => this.auth.forget(d.id) },
      ],
    });
    await alert.present();
  }

  protected disconnect(): void {
    this.connection.disconnect();
    void this.router.navigate(['/discover'], { replaceUrl: true });
  }
}
