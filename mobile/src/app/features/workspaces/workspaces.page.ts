import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonHeader, IonToolbar, IonTitle, IonContent, IonList, IonItem, IonLabel, IonBadge,
  IonButtons, IonButton, IonIcon, IonNote, IonRefresher, IonRefresherContent,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { logOutOutline, ellipse } from 'ionicons/icons';
import { WorkspaceService } from '../../core/services/workspace.service';
import { ConnectionService } from '../../core/services/connection.service';

@Component({
  selector: 'app-workspaces',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    IonHeader, IonToolbar, IonTitle, IonContent, IonList, IonItem, IonLabel, IonBadge,
    IonButtons, IonButton, IonIcon, IonNote, IonRefresher, IonRefresherContent,
  ],
  template: `
    <ion-header>
      <ion-toolbar>
        <ion-title>Workspaces</ion-title>
        <ion-buttons slot="end">
          <ion-badge [color]="stateColor()">{{ connection.state() }}</ion-badge>
          <ion-button (click)="disconnect()">
            <ion-icon slot="icon-only" name="log-out-outline" />
          </ion-button>
        </ion-buttons>
      </ion-toolbar>
    </ion-header>

    <ion-content>
      <ion-refresher slot="fixed" (ionRefresh)="refresh($event)">
        <ion-refresher-content />
      </ion-refresher>

      <ion-list>
        @for (ws of workspaces.workspaces(); track ws.name) {
          <ion-item button detail="true" (click)="open(ws.name)">
            <ion-icon
              slot="start"
              name="ellipse"
              [color]="ws.running ? 'success' : 'medium'"
              aria-hidden="true"
            />
            <ion-label>
              <h2>{{ ws.name }}</h2>
              <ion-note>{{ ws.agents }} agents · {{ ws.skills }} skills</ion-note>
            </ion-label>
            @if (ws.personaSet) { <ion-badge slot="end" color="tertiary">persona</ion-badge> }
          </ion-item>
        } @empty {
          <ion-item lines="none">
            <ion-label color="medium">
              {{ connection.connected() ? 'No workspaces.' : 'Not connected.' }}
            </ion-label>
          </ion-item>
        }
      </ion-list>
    </ion-content>
  `,
})
export class WorkspacesPage {
  protected readonly workspaces = inject(WorkspaceService);
  protected readonly connection = inject(ConnectionService);
  private readonly router = inject(Router);

  constructor() {
    addIcons({ logOutOutline, ellipse });
  }

  protected open(name: string): void {
    // Drop focus before the route change so Ionic's aria-hidden on the leaving page is valid.
    (document.activeElement as HTMLElement | null)?.blur();
    void this.router.navigate([name, 'chats']);
  }

  protected disconnect(): void {
    this.connection.disconnect();
    void this.router.navigate(['/discover']);
  }

  protected refresh(ev: CustomEvent): void {
    this.workspaces.list();
    setTimeout(() => (ev.target as HTMLIonRefresherElement).complete(), 500);
  }

  protected stateColor(): string {
    switch (this.connection.state()) {
      case 'connected': return 'success';
      case 'reconnecting': return 'warning';
      case 'connecting': return 'warning';
      default: return 'medium';
    }
  }
}
