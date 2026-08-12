import { Component, ChangeDetectionStrategy, inject, signal, Input, OnInit } from '@angular/core';
import {
  IonHeader, IonToolbar, IonTitle, IonContent, IonButtons, IonButton, IonText, IonNote,
  IonSpinner, IonInputOtp, ModalController,
} from '@ionic/angular/standalone';
import { AuthService } from '../../core/services/auth.service';
import { DaemonService } from '../../core/services/daemon.service';

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
          @if (reachable() === 0) {
            No paired device is connected to show the code. Open Nocturn on a device you already have,
            or run <code>nocturn pair</code> on the server for a code of your own.
          } @else {
            Code from an already-paired device (Settings → Pairing requests).
          }
        </ion-input-otp>
        <!-- Never a one-way door: whatever brought us here, there is always a way back to the other
             way in. This is the link whose absence turned an expired code into a trap. -->
        <p class="alt"><button type="button" class="link" (click)="backToCode()">Enter a code from the server instead</button></p>
      } @else if (!bootstrap && !paired) {
        <!-- The state that used to have no screen: no code armed, and nothing that could relay one.
             There is genuinely nothing to type here, so showing a form would be a lie. -->
        <div class="stuck">
          <p>No pairing code is active, and no paired device is available to authorise this one.</p>
          <p>On the machine running Nocturn:</p>
          <pre><code>nocturn pair</code></pre>
          <p>It prints a fresh code and a link. You can run it any time.</p>
          <p class="alt"><button type="button" class="link" (click)="retry()">I've run it — check again</button></p>
        </div>
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
          @if (bootstrap) {
            Code from the machine running Nocturn.
          } @else {
            No code is active — run <code>nocturn pair</code> on the server for one.
          }
        </ion-input-otp>
        @if (paired) {
          <p class="alt">Already have a paired device? <button type="button" class="link" (click)="startJoin()">Ask it instead</button></p>
        }
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
    /* These read as links but they DO something rather than go somewhere, and an <a> with no href is
       neither focusable nor operable by keyboard — so they are buttons wearing a link's clothes. */
    ion-input-otp a, .alt .link {
      color: var(--ion-color-primary); cursor: pointer;
      background: none; border: 0; padding: 0; font: inherit;
    }
    .alt { text-align: center; font-size: 0.85rem; color: var(--ion-color-medium); margin-top: 1rem; }
    .stuck { font-size: 0.9rem; color: var(--ion-color-medium); }
    .stuck pre {
      background: var(--ion-background-color-step-100); padding: 0.75rem 1rem;
      border-radius: 0.5rem; overflow-x: auto; color: var(--ion-text-color);
    }
  `,
})
export class PairPage implements OnInit {
  /** ws:// URL of the daemon to pair with (set via modal componentProps). */
  @Input() url = '';

  /** A pairing code is armed on the daemon right now (from `daemon.json`). */
  @Input() bootstrap = true;

  /**
   * Something in the household can already bring another device in (from `daemon.json`).
   *
   * Together with `bootstrap` this is what decides which of four screens appears. One bit could not:
   * "no code armed" means *ask a paired device* when one exists and *there is no way in at all* when
   * none does, and an earlier version of this page treated both as the first — auto-starting a join
   * that asked for a code no device in existence could display, with no link back.
   */
  @Input() paired = true;

  private readonly auth = inject(AuthService);
  private readonly daemon = inject(DaemonService);
  private readonly modalCtrl = inject(ModalController);

  protected readonly code = signal('');
  protected readonly joinId = signal<string | null>(null);
  protected readonly joinCode = signal('');
  /** How many paired devices are connected to display the join code. 0 means nobody is watching. */
  protected readonly reachable = signal<number | null>(null);
  protected readonly busy = signal(false);
  protected readonly error = signal<string | null>(null);

  ngOnInit(): void {
    // A code handed over in the link `nocturn pair` printed: redeem it and be done. The fragment is
    // scrubbed either way — it never reached the server, and it must not sit in the address bar or
    // survive into a bookmark.
    const code = this.codeFromLink();
    if (code) {
      this.code.set(code);
      void this.pair();
    }
  }

  /**
   * The pairing code from `#c=…`, if this page was opened by the link `nocturn pair` printed.
   *
   * The FRAGMENT specifically: it is never sent to a server, so it appears in no access log and no
   * Referer header. It is scrubbed from the address bar immediately, and it is single-use, five
   * minutes and five attempts anyway — a spent code is worth nothing to whoever finds it in history.
   */
  private codeFromLink(): string | null {
    const match = /(?:^|[#&])c=(\d{6})\b/.exec(location.hash);
    if (!match) return null;
    history.replaceState(null, '', location.pathname + location.search);
    return match[1];
  }

  protected async pair(): Promise<void> {
    await this.run(() => this.auth.pair(this.url, this.code()));
  }

  protected async startJoin(): Promise<void> {
    await this.guard(async () => {
      const { joinId, reachable } = await this.auth.join(this.url);
      this.reachable.set(reachable);
      this.joinId.set(joinId);
    });
  }

  /** Leave the join flow for the code field. The link whose absence made an expired code a trap. */
  protected backToCode(): void {
    this.joinId.set(null);
    this.joinCode.set('');
    this.error.set(null);
  }

  /** Re-ask the daemon after the user has run `nocturn pair`, so the screen can move on by itself. */
  protected async retry(): Promise<void> {
    await this.guard(async () => {
      const info = await this.daemon.probe(this.url);
      if (!info) return;
      this.bootstrap = info.bootstrap;
      this.paired = info.paired;
      if (!info.bootstrap && !info.paired) {
        this.error.set('Still no code. Run `nocturn pair` on the machine running Nocturn.');
      }
    });
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
