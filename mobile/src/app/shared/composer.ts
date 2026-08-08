import { Component, ChangeDetectionStrategy, input, output, signal, viewChild } from '@angular/core';
import { IonToolbar, IonButton, IonInput } from '@ionic/angular/standalone';
import { LucideArrowUp, LucideSquare } from '@lucide/angular';

/**
 * The message composer — the "submit part" shared by the chat detail and the hero. A single-line
 * field and its send button live inside ONE pill, so the pair reads as a single control rather
 * than a field with a button parked next to it. One line, not a growing textarea: the pill is a
 * fixed shape, and a field that silently changes the page's proportions as you type is not.
 *
 * It owns its own draft; the parent gets the trimmed text via `(send)` and (when running)
 * `(cancel)`. It renders only the `<ion-toolbar>`; the parent supplies the `<ion-footer>` around it,
 * and the keyboard-follow is a global rule on that footer (see styles.scss).
 *
 * The button sits in the pill rather than in an `ion-buttons slot="end"` for exactly that reason —
 * a slotted button is a sibling of the toolbar's content and can only ever be outside the field.
 *
 * Two placements, one skin. Docked in the chat's footer the pill sits on the toolbar's bar; on the
 * hero it stands alone over the artwork, so it is a little taller and its hairline carries the
 * accent instead of a neutral edge. Fill, radius language, focus state and the send disc are the
 * same in both — sending from the hero must not hand you a different-looking control on the next
 * screen.
 */
@Component({
  selector: 'app-composer',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonToolbar, IonButton, IonInput, LucideArrowUp, LucideSquare],
  template: `
    <ion-toolbar class="composer">
      <!-- The pill acts as the field's label: a tap on its padding, or on the empty run to the right
           of a short draft, lands on the pill and not on the input, and without this it silently does
           nothing. -->
      <div class="pill" (click)="focusField($event)">
        <ion-input
          #field
          class="composer-input"
          type="text"
          enterkeyhint="send"
          autocapitalize="sentences"
          [placeholder]="placeholder()"
          [value]="draft()"
          (ionInput)="draft.set($any($event.target).value ?? '')"
          (keydown.enter)="$event.preventDefault(); submit()"
          [disabled]="disabled()"
        />
        @if (running()) {
          <ion-button class="act" shape="round" color="danger" (click)="cancel.emit()" aria-label="Stop">
            <svg lucideSquare slot="icon-only" [size]="15" fill="currentColor" />
          </ion-button>
        } @else {
          <ion-button class="act" shape="round" [disabled]="!draft().trim() || disabled()" (click)="submit()" aria-label="Send">
            <svg lucideArrowUp slot="icon-only" [size]="20" [strokeWidth]="2.2" />
          </ion-button>
        }
      </div>
    </ion-toolbar>
  `,
  styles: `
    /* Docked, the toolbar IS the bar the pill sits on, so it keeps the toolbar surface. */
    .composer {
      --padding-start: 10px;
      --padding-end: 10px;
      --padding-top: 6px;
      --padding-bottom: 6px;
    }
    :host(.hero) .composer {
      --background: transparent;
      --min-height: 0;
      --padding-start: 0;
      --padding-end: 0;
      --padding-top: 0;
      --padding-bottom: 0;
    }

    /* Centre, not flex-end. The field and the disc are different heights, so bottom-aligning them
       puts the text's optical centre a few px above the disc's — enough to read as crooked on a
       one-line composer, which is the state it is in almost always. */
    .pill {
      display: flex;
      align-items: center;
      gap: 0.35rem;
      /* The reading measure, shared with the chat transcript above it: on a wide pane the field and
         the messages have to sit in the same column or the composer reads as a separate bar. */
      max-width: var(--nocturn-measure);
      margin-inline: auto;
      position: relative;
      padding: 0.35rem 0.35rem 0.35rem 1rem;
      border-radius: 1.35rem;
      /* Docked default: a lifted surface on the footer's bar, per the design. No blur here — the bar
         behind it is opaque, so there is nothing to see through, and a backdrop-filter would only
         cost a compositing layer. */
      background: var(--ion-background-color-step-50);
      box-shadow: inset 0 0 0 1px var(--ion-background-color-step-150);
    }

    /* The field carries no chrome of its own — the pill is the chrome. */
    .composer-input {
      flex: 1;
      min-width: 0;
      margin: 0;
      /* Ionic keeps a 44px min-height on the host for touch reasons. Here the PILL is the touch
         target — it is taller than 44px and a tap anywhere on it focuses the field — so the field
         itself may collapse to its text and let the send disc set the pill's height. Left in, the
         disc sits in a box 4px taller than itself and the ring around it stops being even. */
      min-height: 0;
      /* The caret would otherwise be painted in the UA's default, which on a dark field is a dark
         bar on a dark ground — indistinguishable from no caret at all. */
      caret-color: var(--ion-color-secondary);
      --background: transparent;
      --border-width: 0;
      --padding-start: 0;
      --padding-end: 0;
      /* No padding of its own: the pill supplies it. Left in, the field's box is taller than its
         text and the placeholder no longer sits on the pill's centre line. */
      --padding-top: 0;
      --padding-bottom: 0;
      --placeholder-color: var(--ion-color-medium);
      --placeholder-opacity: 1;
      /* Ionic's md input draws a focus underline across the field. Inside a pill that reads as a
         stray rule cutting the control in half, so it goes — and the focus state moves to the pill
         below, which is the thing the user actually sees as the input. */
      --highlight-height: 0;
      --highlight-color-focused: transparent;
      --highlight-color-valid: transparent;
      --highlight-color-invalid: transparent;
    }

    /* Hero placement differs in SIZE only. It wore glass — translucent fill, a blur and a drop
       shadow — and every one of those cost something: the shadow read as a smudge under the pill on
       the artwork, the blur put a filter in the input's ancestor chain, and the whole thing gave the
       hero a focus state that looked nothing like the chat's. One skin, two sizes. */
    :host(.hero) .pill {
      /* Taller than the docked one — standing alone on the artwork the pill supplies the breathing
         room the chat's bar supplies there. The three inset values that surround the disc are the
         SAME number: the disc is a circle, so an even ring around it is what reads as nestled, and
         any difference between its side gap and its top gap shows up immediately. */
      padding: 0.5rem 0.5rem 0.5rem 1.15rem;
      border-radius: 1.75rem;
      /* Accent hairline at REST, not just on focus. On the chat's toolbar a neutral edge is enough
         to separate the field from the bar behind it; over the artwork there is no bar, and a grey
         hairline on a purple nebula disappears. The accent is what makes the pill an object. */
      box-shadow: inset 0 0 0 1px rgba(var(--ion-color-primary-rgb), 0.45);
    }

    /* Focus lives on the pill: the hairline turns accent. :focus-within, not one of Ionic's state
       classes — ion-input is SCOPED rather than shadow, so its native input is a real descendant of
       the pill and the browser's own focus state is the honest signal. */
    .pill:focus-within {
      box-shadow: inset 0 0 0 1.5px rgba(var(--ion-color-primary-rgb), 0.65);
    }

    /* Send/stop is a filled disc: one small solid target that reads as the thing that commits,
       against a field that reads as a place to type.
       The DISC is Ionic's, not ours: shape="round" plus a slot="icon-only" child puts the button in
       its icon-only mode, which drops Material's 64px min-width and squares the box — so the round
       shape lands on a square and comes out a circle. Sizing it by hand instead fights that rule and
       yields an ellipse. Only the colours are ours, through ion-button's own CSS variables. */
    .act {
      flex: none;
      margin: 0;
      --background: var(--ion-color-primary);
      --background-activated: var(--ion-color-primary-shade);
      --background-hover: var(--ion-color-primary-tint);
      --color: var(--ion-color-primary-contrast);
    }
    /* The glyph takes the button's contrast colour explicitly. A slotted svg is not ion-icon: it
       does not pick up ion-button's --color on its own, so it would otherwise inherit the field's
       text colour and sit on the accent as a pale lilac arrow instead of a white one. */
    .act svg { color: var(--ion-color-primary-contrast); }
    .act.ion-color-danger svg { color: var(--ion-color-danger-contrast); }
    /* Empty draft dims the disc rather than greying it out. Turning it to a neutral fill reads as a
       different control appearing once you type; keeping the accent and lowering it keeps one
       control that is simply not armed yet. */
    .act.button-disabled {
      opacity: 0.55;
    }
    .act.ion-color-danger {
      --background: var(--ion-color-danger);
      --color: var(--ion-color-danger-contrast);
    }

  `,
})
export class ComposerComponent {
  readonly placeholder = input('Message…');
  readonly disabled = input(false);
  readonly running = input(false);

  readonly send = output<string>();
  readonly cancel = output<void>();

  private readonly field = viewChild.required<IonInput>('field');

  protected readonly draft = signal('');

  /** Tapping the pill anywhere puts the caret in the field — except on the send/stop button, which
      has its own job and must not pull the keyboard back up.
   *
   * Focused through the native input with preventScroll rather than through setFocus(). A plain
   * focus() asks the browser to scroll the field into view, and a scroll container still obeys that
   * SCRIPTED scroll even at overflow: hidden — on the hero, whose stage is a hair taller than the
   * viewport, the whole lockup jumped upward the moment the field was touched. */
  protected async focusField(event: Event): Promise<void> {
    if ((event.target as HTMLElement).closest('.act')) return;
    const native = await this.field().getInputElement();
    native.focus({ preventScroll: true });
  }

  protected submit(): void {
    const text = this.draft().trim();
    if (!text) return;
    this.send.emit(text);
    this.draft.set('');
  }
}
