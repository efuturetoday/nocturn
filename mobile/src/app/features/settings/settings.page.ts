import { Component, ChangeDetectionStrategy, inject, linkedSignal } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonContent, IonList, IonListHeader, IonItem, IonLabel, IonTextarea, IonButton, IonIcon,
  IonNote, IonChip,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { logOutOutline, saveOutline, sparklesOutline } from 'ionicons/icons';
import { WorkspaceService } from '../../core/services/workspace.service';
import { ConnectionService } from '../../core/services/connection.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';

@Component({
  selector: 'app-settings',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    WorkspaceHeaderComponent, IonContent, IonList, IonListHeader, IonItem, IonLabel,
    IonTextarea, IonButton, IonIcon, IonNote, IonChip,
  ],
  template: `
    <app-workspace-header />

    <ion-content class="ion-padding">
      <ion-list inset="true">
        <ion-list-header><ion-label>Persona</ion-label></ion-list-header>
        <ion-item>
          <ion-textarea
            [autoGrow]="true"
            [rows]="4"
            placeholder="System persona for this workspace…"
            [value]="persona()"
            (ionInput)="persona.set($any($event.target).value ?? '')"
          />
        </ion-item>
        <ion-item lines="none">
          <ion-button slot="end" size="small" [disabled]="!dirty()" (click)="savePersona()">
            <ion-icon slot="start" name="save-outline" /> Save
          </ion-button>
        </ion-item>
      </ion-list>

      @if (detail(); as d) {
        <ion-list inset="true">
          <ion-list-header><ion-label>Skills</ion-label></ion-list-header>
          @for (s of d.skills; track s.name) {
            <ion-item>
              <ion-icon slot="start" name="sparkles-outline" color="secondary" aria-hidden="true" />
              <ion-label class="ion-text-wrap">
                <h2>{{ s.name }}</h2>
                <ion-note>{{ s.description }}</ion-note>
              </ion-label>
            </ion-item>
          } @empty {
            <ion-item lines="none"><ion-label color="medium">No skills.</ion-label></ion-item>
          }
        </ion-list>
      }

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
  protected readonly workspaces = inject(WorkspaceService);
  protected readonly connection = inject(ConnectionService);
  private readonly router = inject(Router);

  protected readonly detail = this.workspaces.selected;
  // Editable persona draft that re-seeds whenever the workspace detail changes.
  protected readonly persona = linkedSignal(() => this.workspaces.selected()?.persona ?? '');
  protected readonly dirty = () => this.persona() !== (this.workspaces.selected()?.persona ?? '');

  constructor() {
    addIcons({ logOutOutline, saveOutline, sparklesOutline });
  }

  protected savePersona(): void {
    const ws = this.workspaces.active();
    if (ws) this.workspaces.setPersona(ws, this.persona());
  }

  protected disconnect(): void {
    this.connection.disconnect();
    void this.router.navigate(['/discover'], { replaceUrl: true });
  }
}
