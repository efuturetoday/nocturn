import { Component, ChangeDetectionStrategy, computed, inject } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonContent,
  IonList,
  IonListHeader,
  IonItem,
  IonLabel,
  IonNote,
  IonChip,
  IonSpinner,
} from '@ionic/angular/standalone';
import { Capacitor } from '@capacitor/core';
import { LucideLogOut } from '@lucide/angular';
import { ConnectionService } from '../../core/services/connection.service';
import { AccountsService } from '../../core/services/accounts.service';
import { isDemoUrl } from '../../core/demo/is-demo';

/** The accounts this workspace can connect, and which daemon the app is talking to. */
@Component({
  selector: 'app-settings-general',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [LucideLogOut, IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote, IonChip, IonSpinner],
  template: `
    <ion-content>
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
export class SettingsGeneralPage {
  protected readonly connection = inject(ConnectionService);
  protected readonly accounts = inject(AccountsService);
  private readonly router = inject(Router);

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

  protected disconnect(): void {
    this.connection.disconnect();
    void this.router.navigate(['/discover'], { replaceUrl: true });
  }
}
