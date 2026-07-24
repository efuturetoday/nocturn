import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote, IonChip, IonIcon, IonSpinner,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { logOutOutline } from 'ionicons/icons';
import { ConnectionService } from '../../core/services/connection.service';
import { AuthService } from '../../core/services/auth.service';
import { AccountsService } from '../../core/services/accounts.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';

@Component({
  selector: 'app-settings',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    WorkspaceHeaderComponent, IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote,
    IonChip, IonIcon, IonSpinner,
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

      <ion-list inset="true">
        <ion-list-header><ion-label>Accounts</ion-label></ion-list-header>
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
      </ion-list>

      <ion-list inset="true">
        <ion-list-header><ion-label>Connection</ion-label></ion-list-header>
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
          <ion-icon slot="start" name="log-out-outline" color="danger" />
          <ion-label color="danger">Disconnect</ion-label>
        </ion-item>
      </ion-list>
    </ion-content>
  `,
})
export class SettingsPage {
  protected readonly connection = inject(ConnectionService);
  protected readonly auth = inject(AuthService);
  protected readonly accounts = inject(AccountsService);
  private readonly router = inject(Router);

  constructor() {
    addIcons({ logOutOutline });
  }

  protected connect(server: string): void {
    this.accounts.connect(server);
  }

  protected disconnect(): void {
    this.connection.disconnect();
    void this.router.navigate(['/discover'], { replaceUrl: true });
  }
}
