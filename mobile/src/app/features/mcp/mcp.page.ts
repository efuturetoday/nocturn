import { Component, ChangeDetectionStrategy, inject, signal, computed } from '@angular/core';
import {
  IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote, IonChip, IonButton, IonSpinner,
  IonModal, IonHeader, IonToolbar, IonTitle, IonButtons, IonInput, IonSelect, IonSelectOption,
  AlertController,
} from '@ionic/angular/standalone';
import { LucidePlus, LucideX, LucideStore } from '@lucide/angular';
import { LibraryModalComponent } from '../library/library-modal';
import { McpService } from '../../core/services/mcp.service';
import { AccountsService } from '../../core/services/accounts.service';
import type { MCPInfo, MCPState } from '../../core/protocol/nocturn-protocol';

/** The daemon's own rule for a server name, checked here so a typo is answered by the field that
    holds it. The name becomes a folder, a secret shard key and a tool-name prefix. */
const VALID_NAME = /^[a-z0-9][a-z0-9_-]{0,31}$/;

/**
 * The active workspace's MCP servers: what is declared, what came up, and what to do about the rest.
 *
 * The list reports DECLARED servers, not connected ones — a server you configured that did not come
 * up is exactly what this page is opened to find. So the state chip is the content of every row, and
 * each state gets the one action that resolves it: `needs auth` is an errand, not a fault, and leads
 * into the account flow the app already has; `failed` leads to trying again.
 *
 * Adding one shows `connecting` first and the outcome up to thirty seconds later, because that is
 * what the daemon sends. Nothing here waits for the second frame — a row that appeared instantly and
 * then told the truth is better than a spinner over a list.
 */
@Component({
  selector: 'app-mcp',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    LibraryModalComponent, LucidePlus, LucideX, LucideStore,
    IonContent, IonList, IonListHeader, IonItem, IonLabel, IonNote, IonChip, IonButton, IonSpinner,
    IonModal, IonHeader, IonToolbar, IonTitle, IonButtons, IonInput, IonSelect, IonSelectOption,
  ],
  template: `
    <ion-content>
      <ion-list inset="true">
        <ion-list-header><ion-label>MCP Servers</ion-label></ion-list-header>
        @for (s of mcp.servers(); track s.name) {
          <ion-item lines="full" class="row">
            <ion-label class="ion-text-wrap">
              <h2>{{ s.name }}</h2>
              <p class="url">{{ s.url }}</p>
              @if (s.note) {
                <ion-note [color]="s.state === 'failed' ? 'danger' : 'warning'">{{ s.note }}</ion-note>
              } @else if (s.state === 'connected') {
                <ion-note>{{ s.tools }} {{ s.tools === 1 ? 'tool' : 'tools' }}</ion-note>
              }
            </ion-label>

            <ion-chip slot="end" [color]="chipColor(s.state)">
              @if (s.state === 'connecting') {
                <ion-spinner name="dots" />
              }
              {{ s.state }}
            </ion-chip>

            @if (s.state === 'needs auth') {
              <ion-button slot="end" fill="clear" [disabled]="accounts.busy()" (click)="signIn(s)" [attr.aria-label]="'Connect ' + s.name">
                Connect
              </ion-button>
            } @else if (s.state === 'failed') {
              <ion-button slot="end" fill="clear" (click)="mcp.reload()" [attr.aria-label]="'Retry ' + s.name">Retry</ion-button>
            }
            <ion-button slot="end" fill="clear" color="danger" (click)="remove(s)" [attr.aria-label]="'Remove ' + s.name">Remove</ion-button>
          </ion-item>
        } @empty {
          <ion-item lines="none">
            <ion-label color="medium">
              No servers declared. Browse the Library below, or add one by hand.
            </ion-label>
          </ion-item>
        }

        <!-- The catalog first; manual second, because a curated catalog will never hold the server
             somebody runs on their own network. -->
        <ion-item button lines="full" (click)="browsing.set(true)">
          <svg lucideStore slot="start" [size]="21" class="add" />
          <ion-label color="primary">Browse the Library…</ion-label>
        </ion-item>
        <ion-item button lines="none" (click)="add()">
          <svg lucidePlus slot="start" [size]="21" class="add" />
          <ion-label color="primary">Add manually…</ion-label>
        </ion-item>
      </ion-list>

      <p class="hint">
        Tokens are not set here: seed one on the host with <code>nocturn secret set</code>. An OAuth
        server is signed into from its row. Changes take effect on the next message.
      </p>

      <app-library-modal [(open)]="browsing" initial="mcp" />

      <!-- A form, not an alert: an Ionic alert cannot hold text fields and a choice at the same
           time, and the auth mode is a choice with three answers. -->
      <ion-modal [isOpen]="adding()" (didDismiss)="adding.set(false)">
        <ng-template>
          <ion-header>
            <ion-toolbar>
              <ion-title>Add an MCP server</ion-title>
              <ion-buttons slot="end">
                <ion-button (click)="adding.set(false)" aria-label="Close">
                  <svg lucideX [size]="22" />
                </ion-button>
              </ion-buttons>
            </ion-toolbar>
          </ion-header>
          <ion-content>
            <ion-list inset="true">
              <ion-item>
                <ion-input
                  label="Name"
                  labelPlacement="stacked"
                  placeholder="acme"
                  autocapitalize="off"
                  [value]="name()"
                  (ionInput)="name.set($any($event.target).value ?? '')"
                />
              </ion-item>
              <ion-item>
                <ion-input
                  label="URL"
                  labelPlacement="stacked"
                  type="url"
                  placeholder="https://acme.example/mcp"
                  autocapitalize="off"
                  [value]="url()"
                  (ionInput)="url.set($any($event.target).value ?? '')"
                />
              </ion-item>
              <ion-item>
                <ion-select
                  label="Authentication"
                  labelPlacement="stacked"
                  interface="popover"
                  [value]="auth()"
                  (ionChange)="auth.set($any($event.detail).value)"
                >
                  <ion-select-option value="">None</ion-select-option>
                  <ion-select-option value="bearer">Bearer token (seeded on the host)</ion-select-option>
                  <ion-select-option value="oauth">OAuth</ion-select-option>
                </ion-select>
              </ion-item>
            </ion-list>

            @if (problem(); as why) {
              <p class="why">{{ why }}</p>
            }

            <ion-button expand="block" class="submit" [disabled]="problem() !== null" (click)="submit()">
              Add
            </ion-button>
          </ion-content>
        </ng-template>
      </ion-modal>
    </ion-content>
  `,
  styles: `
    .add { color: var(--ion-color-primary); }
    .row .url { font-size: 0.8rem; word-break: break-all; }
    ion-chip ion-spinner { width: 0.9rem; height: 0.9rem; margin-inline-end: 0.35rem; }
    .submit { margin: 0.5rem 1rem; }
    .why { margin: 0 1.25rem; color: var(--ion-color-medium); font-size: 0.85rem; }
    .hint {
      max-width: min(var(--nocturn-measure), calc(100% - 2rem));
      margin-inline: auto;
      margin-block: 0;
      color: var(--ion-color-medium);
      font-size: 0.85rem;
    }
    .hint code { font-size: 0.8rem; }
  `,
})
export class McpPage {
  protected readonly mcp = inject(McpService);
  protected readonly accounts = inject(AccountsService);
  private readonly alerts = inject(AlertController);

  /** The chip carries the state, so its colour must not overstate one: `needs auth` is an errand. */
  protected chipColor(state: MCPState): string {
    switch (state) {
      case 'connected':
        return 'success';
      case 'needs auth':
        return 'warning';
      case 'failed':
        return 'danger';
      default:
        return 'medium';
    }
  }

  /** Sign in through the flow the app already has, then re-run discovery so the tools appear. */
  protected signIn(s: MCPInfo): void {
    this.accounts.connect(s.name);
    this.mcp.reload();
  }

  protected readonly browsing = signal(false);
  protected readonly adding = signal(false);
  protected readonly name = signal('');
  protected readonly url = signal('');
  protected readonly auth = signal('');

  /**
   * What is wrong with the form, in one sentence, or null when it is ready. The daemon validates
   * either way — this says it before the round-trip, and says WHICH rule, because "invalid name" on
   * a field with no visible rule is a puzzle rather than an answer.
   */
  protected readonly problem = computed<string | null>(() => {
    const name = this.name().trim();
    const url = this.url().trim();
    if (!name || !url) return 'A name and a URL are needed.';
    if (!VALID_NAME.test(name)) {
      return 'The name must be lowercase letters, digits, - or _, starting with a letter or digit (up to 32).';
    }
    if (!url.startsWith('https://')) return 'The URL must be https — a token would otherwise travel in clear.';
    return null;
  });

  /** Open the form empty: the previous attempt's values are never what the next server wants. */
  protected add(): void {
    this.name.set('');
    this.url.set('');
    this.auth.set('');
    this.adding.set(true);
  }

  protected submit(): void {
    if (this.problem() !== null) return;
    this.mcp.add(this.name().trim(), this.url().trim(), this.auth() || undefined);
    this.adding.set(false);
  }

  /**
   * Drop a server, behind a confirmation that says the part nobody would guess: the remembered
   * network permission for its host goes too. That grant may have been given for `http_read` on the
   * same host, so this can cost an answer you gave for something else — you are asked once more.
   */
  protected async remove(s: MCPInfo): Promise<void> {
    const alert = await this.alerts.create({
      header: `Remove ${s.name}?`,
      message:
        `Its declaration and any token stored for it are deleted, and the remembered permission to ` +
        `reach ${this.hostOf(s.url)} is revoked — if you had allowed that host for something else, ` +
        `Nocturn will ask about it once more.`,
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        { text: 'Remove', role: 'destructive', handler: () => this.mcp.remove(s.name) },
      ],
    });
    await alert.present();
  }

  /** The host as the daemon computes it for the grant. Falls back to the raw URL rather than lying
      about which permission is at stake. */
  private hostOf(url: string): string {
    try {
      return new URL(url).host;
    } catch {
      return url;
    }
  }
}
