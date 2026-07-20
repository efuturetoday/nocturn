import { Component, ChangeDetectionStrategy, inject, linkedSignal } from '@angular/core';
import { Router } from '@angular/router';
import {
  IonContent, IonList, IonListHeader, IonItem, IonLabel, IonTextarea, IonButton, IonIcon,
  IonNote, IonChip, IonItemSliding, IonItemOptions, IonItemOption, IonAccordion, IonAccordionGroup,
  AlertController,
} from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import {
  logOutOutline, saveOutline, trashOutline, notificationsOutline,
  logoApple, logoAndroid, globeOutline,
} from 'ionicons/icons';
import { WorkspaceService } from '../../core/services/workspace.service';
import { ConnectionService } from '../../core/services/connection.service';
import { AuthService } from '../../core/services/auth.service';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import { relativeTime } from '../chat/components/chat-row';
import type { DeviceMeta } from '../../core/protocol/nocturn-protocol';

@Component({
  selector: 'app-settings',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    WorkspaceHeaderComponent, IonContent, IonList, IonListHeader, IonItem, IonLabel,
    IonTextarea, IonButton, IonIcon, IonNote, IonChip, IonItemSliding, IonItemOptions, IonItemOption,
    IonAccordion, IonAccordionGroup,
  ],
  styles: `
    .time { color: var(--ion-color-medium); font-size: 0.78rem; }
    /* Skill description lives in the accordion's expanded content — compact, secondary. */
    .skill-desc {
      padding: 0.25rem 1rem 0.875rem;
      color: var(--ion-color-medium); font-size: 0.85rem; line-height: 1.45;
    }
  `,
  template: `
    <app-workspace-header />

    <ion-content>
      <ion-list inset="true">
        <ion-list-header>
          <ion-label>Persona</ion-label>
          @if (dirty()) {
            <ion-button slot="end" size="small" fill="clear" (click)="savePersona()">
              <ion-icon slot="start" name="save-outline" /> Save
            </ion-button>
          }
        </ion-list-header>
        <ion-item lines="none">
          <ion-textarea
            [autoGrow]="true"
            [rows]="4"
            placeholder="System persona for this workspace…"
            [value]="persona()"
            (ionInput)="persona.set($any($event.target).value ?? '')"
          />
        </ion-item>
      </ion-list>

      @if (detail(); as d) {
        <ion-list inset="true">
          <ion-list-header>
            <ion-label>Skills</ion-label>
            @if (d.skills.length) { <ion-note slot="end">{{ d.skills.length }}</ion-note> }
          </ion-list-header>
          @if (d.skills.length) {
            <ion-accordion-group>
              @for (s of d.skills; track s.name) {
                <ion-accordion [value]="s.name">
                  <ion-item slot="header"><ion-label>{{ s.name }}</ion-label></ion-item>
                  <div slot="content" class="skill-desc">{{ s.description }}</div>
                </ion-accordion>
              }
            </ion-accordion-group>
          } @else {
            <ion-item lines="none"><ion-label color="medium">No skills.</ion-label></ion-item>
          }
        </ion-list>
      }

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
        <ion-list-header><ion-label>Paired devices</ion-label></ion-list-header>
        @for (d of auth.devices(); track d.id) {
          <ion-item-sliding>
            <ion-item>
              <ion-icon slot="start" [name]="platformIcon(d.platform)" color="medium" aria-hidden="true" />
              <ion-label>
                <h2>{{ d.name }}</h2>
                <ion-note>
                  {{ d.platform }}
                  @if (d.hasPush) { · <ion-icon name="notifications-outline" aria-label="push enabled" /> }
                </ion-note>
              </ion-label>
              <span slot="end" class="time">{{ d.lastUsed ? ago(d.lastUsed) : 'never' }}</span>
            </ion-item>
            <ion-item-options side="end">
              <ion-item-option color="danger" (click)="revoke(d)">
                <ion-icon slot="icon-only" name="trash-outline" />
              </ion-item-option>
            </ion-item-options>
          </ion-item-sliding>
        } @empty {
          <ion-item lines="none"><ion-label color="medium">No paired devices.</ion-label></ion-item>
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
  protected readonly workspaces = inject(WorkspaceService);
  protected readonly connection = inject(ConnectionService);
  protected readonly auth = inject(AuthService);
  private readonly router = inject(Router);
  private readonly alerts = inject(AlertController);
  protected readonly ago = relativeTime;

  protected readonly detail = this.workspaces.selected;
  // Editable persona draft that re-seeds whenever the workspace detail changes.
  protected readonly persona = linkedSignal(() => this.workspaces.selected()?.persona ?? '');
  protected readonly dirty = () => this.persona() !== (this.workspaces.selected()?.persona ?? '');

  constructor() {
    addIcons({
      logOutOutline, saveOutline, trashOutline, notificationsOutline,
      logoApple, logoAndroid, globeOutline,
    });
  }

  protected platformIcon(platform?: string): string {
    if (platform === 'ios') return 'logo-apple';
    if (platform === 'android') return 'logo-android';
    return 'globe-outline';
  }

  protected async revoke(d: DeviceMeta): Promise<void> {
    const alert = await this.alerts.create({
      header: 'Unpair device?',
      message: `${d.name} will lose access on its next reconnect.`,
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        { text: 'Unpair', role: 'destructive', handler: () => this.auth.revokeDevice(d.id) },
      ],
    });
    await alert.present();
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
