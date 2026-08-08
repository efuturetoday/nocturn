import { Component, ChangeDetectionStrategy, inject, computed, signal } from '@angular/core';
import { IonContent, IonMenuButton, NavController } from '@ionic/angular/standalone';
import { LucideMenu } from '@lucide/angular';
import { ChatService } from '../../core/services/chat.service';
import { ConnectionService } from '../../core/services/connection.service';
import { ComposerComponent } from '../../shared/composer';
import { heroToChat } from '../../shared/hero-transition';

/**
 * The landing page: one input over the Nocturn galaxy. It has no header — the menu button floats,
 * so nothing competes with the artwork — and no list, because the drawer holds the history.
 *
 * The keyboard is not handled here AT ALL, on purpose. With `resize: none` nothing shrinks, with
 * `scrollPadding: false` Ionic adds no offset, and the mascot is sized in vh — which only `resize:
 * native` would disturb. So the keys simply slide over the lower part of the page and the lockup
 * does not move a pixel. There is room: the pill sits at the vertical centre.
 *
 * The content never scrolls (`--overflow: hidden`), which is what lets the backdrop be an absolutely
 * positioned child rather than a fixed-slot layer fighting the FAB's stacking rules.
 *
 * The backdrop's own styles live in src/theme/galaxy.css — angular.json caps component styles at 4 kB.
 */
@Component({
  selector: 'app-home',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonContent, IonMenuButton, ComposerComponent, LucideMenu],
  template: `
    <!-- autoHide is left at its default ON: once ion-split-pane pins the drawer open (lg and up)
         the menu stops being an overlay, and Ionic takes the button away by itself. A burger that
         toggles a sidebar already on screen is a control with nothing to do. -->
    <ion-menu-button class="burger" menu="main">
      <svg lucideMenu [size]="24" />
    </ion-menu-button>

    <!-- hero-page, not hero. The name hero is the composer's skin contract (see composer.ts) and is
         applied to app-composer further down; one name on both would have this page's scoped rule
         match the composer host too, handing it the page background through inheritance. -->
    <ion-content
      [fullscreen]="true"
      class="hero-page"
    >
      <div class="galaxy" aria-hidden="true">
        <div class="plate"></div>
        <div class="stars stars-1"></div>
        <div class="stars stars-2"></div>
        <div class="stars stars-3"></div>
        <div class="bloom"></div>
        <div class="vignette"></div>
        <div class="floor"></div>
      </div>

      <div class="stage">
        <div class="brand">
          <img src="/assets/brand/mascot.png" width="132" height="132" alt="" />
          <h1>nocturn</h1>
          <p>{{ greeting() }}</p>
        </div>

        <div class="ask">
          <app-composer
            class="hero"
            placeholder="Ask Nocturn…"
            [disabled]="!connection.connected()"
            (send)="start($event)"
          />
        </div>
      </div>
    </ion-content>
  `,
  styles: `
    /* The burger floats over artwork, so it earns a scrim disc — a bare glyph disappears the moment
       a bright part of the nebula drifts under it. Safe-area inset by hand: there is no ion-header
       here to apply it. */
    .burger {
      position: absolute;
      z-index: 10;
      top: calc(var(--ion-safe-area-top, 0px) + 0.5rem);
      left: 0.5rem;
      width: 2.75rem;
      height: 2.75rem;
      --border-radius: 50%;
      --background: rgba(var(--ion-background-color-rgb), 0.42);
      --color: var(--ion-text-color);
      --padding-start: 0;
      --padding-end: 0;
      backdrop-filter: blur(10px);
      border-radius: 50%;
      box-shadow: inset 0 0 0 1px rgba(var(--ion-text-color-rgb), 0.1);
    }

    /* The page is a single screenful by definition; nothing here scrolls. */
    .hero-page { --background: var(--ion-background-color); --overflow: hidden; }

    .stage {
      position: relative;
      z-index: 1;
      min-height: 100%;
      display: flex;
      flex-direction: column;
      align-items: center;
      padding: 0 1.5rem;
      /* Centred, but never past the top edge. Plain centring overflows in BOTH directions on a short
         viewport (landscape, or a small phone with the keyboard up), and since this page does not
         scroll, whatever spills above the top is simply gone — the mascot loses its head. The safe
         keyword falls back to flex-start instead of clipping. The plain value stays underneath it as
         the fallback: an engine that does not know the keyword drops the whole declaration. */
      justify-content: center;
      justify-content: safe center;
    }

    /* Centred here, NOT on .stage: text-align inherits into the composer's field, and a centred
       placeholder in a left-reading input looks like a bug. */
    .brand {
      display: flex;
      flex-direction: column;
      align-items: center;
      text-align: center;
      margin-bottom: 1.6rem;
    }
    /* The mascot is sized off the VIEWPORT HEIGHT, not a breakpoint: it is the thing that has to give
       when there is no room, and the thing that should take the room on an iPad. The floor of the
       clamp keeps it from turning into a speck in landscape; the ceiling keeps it from dominating a
       large screen. */
    .brand img {
      width: clamp(72px, 20vh, 300px);
      height: auto;
      /* No drop-shadow. A drop-shadow traces the ALPHA, and this alpha is a soft nebula cloud the
         size of the whole image — so the "shadow" comes out as a broad dark veil laid over the
         artwork it is supposed to sit in front of, with the image box's edge showing where it stops.
         The mascot already separates itself: it carries its own glow, and the bloom layer sits
         behind it. Dropping the filter also drops a compositing layer the parallax had to move. */
      animation: nocturn-float 6s ease-in-out infinite;
    }
    .brand h1 {
      margin: 0.5rem 0 0;
      font-family: var(--font-display);
      /* Scales with the mascot so the lockup keeps its proportions on a tablet. */
      font-size: clamp(1.6rem, 4.2vh, 3rem);
      font-weight: 500;
      letter-spacing: -0.03em;
      line-height: 1;
    }
    .brand p {
      margin: 0.4rem 0 0;
      color: var(--ion-background-color-step-800);
      font-size: 1rem;
    }

    @keyframes nocturn-float {
      0%, 100% { transform: translateY(0) rotate(-0.6deg); }
      50%      { transform: translateY(-9px) rotate(0.6deg); }
    }
    @media (prefers-reduced-motion: reduce) {
      .brand img { animation: none; }
    }


    /* Just the measure. The pill itself — glass, hairline, lift — belongs to the composer's hero
       skin, so the chat's docked copy and this one can never drift apart. */
    .ask {
      /* Only the full width for the composer to fill — the composer caps itself at the shared
         660px measure, the same column the chat transcript uses. */
      width: 100%;
    }
  `,
})
export class HomePage {
  private readonly chat = inject(ChatService);
  protected readonly connection = inject(ConnectionService);
  private readonly nav = inject(NavController);

  // Read once per page open, not per change detection: the greeting must not flip mid-session
  // because a turn happened to be rendered a minute after midnight.
  private readonly hour = signal(new Date().getHours());
  protected readonly greeting = computed(() => {
    const h = this.hour();
    if (h < 5) return 'Still up?';
    if (h < 12) return 'Good morning.';
    if (h < 18) return 'Good afternoon.';
    return 'Good evening.';
  });

  /** Start a fresh chat: mint a client id, queue the first message, navigate to the detail.
   *
   * Through NavController rather than the Router, because only NavController carries a per-navigation
   * animation. This one hand-off gets the galaxy-dissolve (shared/hero-transition.ts); every other
   * route in the app keeps Ionic's default. */
  protected start(text: string): void {
    const q = text.trim();
    if (!q) return;
    const id = this.chat.newChat();
    this.chat.queueFirstMessage(q);
    void this.nav.navigateForward(['/app/chat', id], { animation: heroToChat });
  }
}
