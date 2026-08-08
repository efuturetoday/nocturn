import { createAnimation, type Animation, type AnimationBuilder } from '@ionic/angular/standalone';

/**
 * The hero → chat transition: a CAMERA PAN DOWNWARD through one scene.
 *
 * Nothing here slides over anything. The camera tilts down, so everything in the world rises out of
 * frame at a rate set by its distance — the nebula plate barely creeps, the three star fields
 * separate, and the mascot and the input, being nearest, leave fastest. Below them, coming into
 * frame from underneath, is the conversation.
 *
 * The one rule that makes it a camera rather than a card: THE CHAT PAGE TRAVELS AT EXACTLY THE SPEED
 * OF THE NEAR PLANE, on exactly the same curve. It sits at the same depth as the mascot and the
 * input, so it has to move like them. Give it its own timing — a different easing, a head start, a
 * stagger of its header and composer — and the eye immediately reads two layers: a page arriving on
 * top of another page. Locked to the near plane it reads as the next thing further down in the same
 * world, which is the whole point.
 *
 * For the same reason nothing fades. A camera does not dissolve what it pans away from; things leave
 * the frame. The one exception is nothing at all — the rising page is opaque and covers the sky by
 * itself.
 *
 * One curve for everything, so the whole frame accelerates and settles together, the way a real
 * camera move does. Single-segment keyframes throughout, which is also why the easing can sit on the
 * animations here: an easing only re-times keyframe offsets when there are intermediate offsets to
 * re-time, and there are none.
 *
 * It is a ROUTE transition, not a single page that morphs. The hero and the chat stay separate
 * components with separate lifecycles; this only replaces the animation Ionic would otherwise play
 * between them, so the routing, the back stack and both components are untouched.
 *
 * Passed per navigation (NavController.navigateForward({ animation })), never registered globally:
 * every other route keeps Ionic's platform-correct default.
 */
const DURATION = 780;
/** The camera's own curve: it takes a moment to get moving and settles rather than stopping. */
const CAMERA = 'cubic-bezier(0.45, 0, 0.15, 1)';

/**
 * How far each plane travels, as a fraction of the camera's throw. 1 is the near plane — the input,
 * and the chat page arriving from below. The numbers ARE the depth of the scene.
 *
 * They fall off steeply on purpose. Spaced evenly (0.2, 0.35, 0.5, 0.75) the frame moves as one
 * sheet with a slight lag and reads as a paper roll being turned; parallax goes inverse to DISTANCE,
 * so the far plate has to be almost nailed down — 0.05 — while the near plane travels the full
 * throw. The eye reads the SIZE of the gaps as depth, not their ordering.
 */
const DEPTH = {
  plate: 0.05,
  stars1: 0.13,
  stars2: 0.28,
  stars3: 0.55,
  bloom: 0.8,
  /** The mascot is near, but not AT the glass — a hair behind the input that sits on it. */
  brand: 0.9,
} as const;

/** Whether the reader has asked for less movement. Read per transition, not cached — it can change. */
function reducedMotion(): boolean {
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}

/**
 * The hero's planes, nearest first. The vignette and the floor gradient are deliberately absent:
 * they are atmosphere belonging to the frame, not objects in the world, and the scene darkening
 * toward the bottom is exactly what the conversation rises out of.
 */
const PLANES: ReadonlyArray<readonly [selector: string, depth: number]> = [
  ['.ask', 1],
  ['.burger', 1],
  ['.brand', DEPTH.brand],
  ['.bloom', DEPTH.bloom],
  ['.stars-3', DEPTH.stars3],
  ['.stars-2', DEPTH.stars2],
  ['.stars-1', DEPTH.stars1],
  ['.plate', DEPTH.plate],
];

/**
 * One camera move, in either direction.
 *
 * `down` is hero → chat: the world rises out of frame and the conversation comes up from below.
 * `up` is the way back: the conversation drops out of frame and the world settles back down into it.
 * The two are the same move played against a sign, which is the point — leaving a chat should undo
 * the gesture that opened it, not play a different animation that happens to end in the same place.
 */
function cameraMove(
  direction: 'down' | 'up',
  enteringEl: HTMLElement,
  leavingEl: HTMLElement | undefined,
  hero: HTMLElement | undefined,
  chat: HTMLElement | undefined,
): Animation {
  const root = createAnimation(`camera-${direction}`).easing(CAMERA).duration(DURATION).addElement(enteringEl);

  // Reduced motion still needs the entering page made visible — Ionic leaves it hidden until an
  // animation touches it — but nothing has to travel to get there.
  if (reducedMotion()) {
    return root.duration(0).beforeRemoveClass('ion-page-invisible').fromTo('opacity', '1', '1');
  }

  // The camera's throw, in pixels: one screenful.
  //
  // Pixels, not percentages. A percentage translateY resolves against the ELEMENT'S OWN height, so
  // the same `-100%` moves the full-bleed nebula by a screenful and the input pill by its own 52px.
  // The near plane has to travel exactly as far as the chat page or the two are not welded, and
  // depth only means anything if every plane measures its share of the SAME distance.
  const throwPx = (leavingEl ?? enteringEl).getBoundingClientRect().height;
  const sign = direction === 'down' ? -1 : 1;

  // The chat page is the near plane's other half: off-frame below when the camera is up at the
  // hero, in frame when it has panned down.
  //
  // beforeRemoveClass is load-bearing. Ionic parks the entering page at ion-page-invisible
  // (opacity: 0) and only lifts it when the transition finishes. Its own transitions animate
  // opacity, so they never notice; this one animates transform only, so without it the page travels
  // the whole way invisible and snaps in at the end — the animation runs perfectly and looks like no
  // animation at all.
  const parts: Animation[] = [
    createAnimation().addElement(enteringEl).beforeRemoveClass('ion-page-invisible'),
  ];

  // Guarded like the hero below it. Ionic omits leavingEl when there is nothing to leave — a cold
  // start straight onto a route, or a replaced history entry — and substituting the entering page
  // for the missing one would translate the arriving hero off the bottom of the screen as if it were
  // the chat.
  if (chat) {
    parts.push(
      createAnimation()
        .addElement(chat)
        .fromTo(
          'transform',
          direction === 'down' ? `translateY(${throwPx}px)` : 'translateY(0)',
          direction === 'down' ? 'translateY(0)' : `translateY(${throwPx}px)`,
        ),
    );
  }

  if (hero) {
    for (const [selector, depth] of PLANES) {
      const el = hero.querySelector(selector);
      if (!el) continue;
      const away = `translateY(${Math.round(sign * throwPx * depth)}px)`;
      parts.push(
        createAnimation()
          .addElement(el)
          .fromTo('transform', direction === 'down' ? 'translateY(0)' : away, direction === 'down' ? away : 'translateY(0)'),
      );
    }
  }

  return root.addAnimation(parts);
}

/** Hero → chat: the camera pans down. */
export const heroToChat: AnimationBuilder = (_baseEl: HTMLElement, opts: unknown): Animation => {
  const { enteringEl, leavingEl } = opts as { enteringEl: HTMLElement; leavingEl?: HTMLElement };
  return cameraMove('down', enteringEl, leavingEl, leavingEl, enteringEl);
};

/** Chat → hero: the same move, reversed. The camera pans back up to the sky. */
export const chatToHero: AnimationBuilder = (_baseEl: HTMLElement, opts: unknown): Animation => {
  const { enteringEl, leavingEl } = opts as { enteringEl: HTMLElement; leavingEl?: HTMLElement };
  return cameraMove('up', enteringEl, leavingEl, enteringEl, leavingEl);
};
