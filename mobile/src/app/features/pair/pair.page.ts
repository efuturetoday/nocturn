import { Component, ChangeDetectionStrategy, inject, signal, Input } from '@angular/core';
import {
  IonHeader, IonToolbar, IonTitle, IonContent, IonButtons, IonButton, IonText, IonNote,
  IonSpinner, IonInputOtp, ModalController,
} from '@ionic/angular/standalone';
import { AuthService } from '../../core/services/auth.service';

/**
 * Pairing overlay (presented as a modal from Discover). Redeem the daemon's 6-digit code
 * (→ /pair) or join an already-paired daemon by relaying a code (→ /join → /join/confirm).
 * On success it dismisses with the bearer; the caller connects. `url` is passed via componentProps.
 */
@Component({
  selector: 'app-pair',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    IonHeader, IonToolbar, IonTitle, IonContent, IonButtons, IonButton, IonText, IonNote,
    IonSpinner, IonInputOtp,
  ],
  template: `
    <ion-header>
      <ion-toolbar>
        <ion-title>Pair device</ion-title>
        <ion-buttons slot="end"><ion-button (click)="cancel()">Cancel</ion-button></ion-buttons>
      </ion-toolbar>
    </ion-header>

    <ion-content class="ion-padding">
      <ion-note class="host">{{ url }}</ion-note>

      @if (joinId()) {
        <ion-input-otp
          [length]="6"
          type="number"
          [disabled]="busy()"
          [value]="joinCode()"
          (ionFocus)="expand()"
          (ionInput)="joinCode.set($any($event.detail).value ?? '')"
          (ionComplete)="confirmJoin()"
        >
          Code from an already-paired device (Settings → Pairing requests).
        </ion-input-otp>
      } @else {
        <ion-input-otp
          [length]="6"
          type="number"
          [disabled]="busy()"
          [value]="code()"
          (ionFocus)="expand()"
          (ionInput)="code.set($any($event.detail).value ?? '')"
          (ionComplete)="pair()"
        >
          Code from the daemon. Already have a paired device? <a (click)="startJoin()">Join</a>
        </ion-input-otp>
      }

      @if (busy()) { <div class="center"><ion-spinner name="crescent" /></div> }
      @if (error(); as err) {
        <ion-text color="danger"><p class="err">{{ err }}</p></ion-text>
      }
    </ion-content>
  `,
  styles: `
    .host { display: block; text-align: center; margin: 0.25rem 0 1rem; word-break: break-all; }
    .center, .err { text-align: center; }
    ion-input-otp { justify-content: center; }
    ion-input-otp a { color: var(--ion-color-primary); cursor: pointer; }
  `,
})
export class PairPage {
  /** ws:// URL of the daemon to pair with (set via modal componentProps). */
  @Input() url = '';

  private readonly auth = inject(AuthService);
  private readonly modalCtrl = inject(ModalController);

  protected readonly code = signal('');
  protected readonly joinId = signal<string | null>(null);
  protected readonly joinCode = signal('');
  protected readonly busy = signal(false);
  protected readonly error = signal<string | null>(null);

  protected async pair(): Promise<void> {
    await this.run(() => this.auth.pair(this.url, this.code()));
  }

  protected async startJoin(): Promise<void> {
    await this.guard(async () => this.joinId.set(await this.auth.join(this.url)));
  }

  protected async confirmJoin(): Promise<void> {
    const jid = this.joinId();
    if (jid) await this.run(() => this.auth.joinConfirm(this.url, jid, this.joinCode()));
  }

  protected cancel(): void {
    void this.modalCtrl.dismiss(null, 'cancel');
  }

  /** Lift the sheet to full height on focus so the keyboard can't cover the code input. */
  protected async expand(): Promise<void> {
    const modal = await this.modalCtrl.getTop();
    await modal?.setCurrentBreakpoint(0.9);
  }

  /** Run a bearer-producing action → dismiss with the bearer so the caller can connect. */
  private async run(action: () => Promise<string>): Promise<void> {
    await this.guard(async () => {
      const bearer = await action();
      await this.modalCtrl.dismiss(bearer, 'paired');
    });
  }

  private async guard(fn: () => Promise<void>): Promise<void> {
    this.busy.set(true);
    this.error.set(null);
    try {
      await fn();
    } catch (e) {
      this.error.set(e instanceof Error ? e.message : 'Pairing failed');
    } finally {
      this.busy.set(false);
    }
  }
}
