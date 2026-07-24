import { Directive, inject } from '@angular/core';
import { KeyboardService } from '../core/services/keyboard.service';

/**
 * `[kbFollow]` lifts a footer up with the on-screen keyboard: it host-binds
 * `translateY(-keyboardHeight)` + a `--kb-fill` var (the strip below the lifted footer, drawn by the
 * `.kb-follow::after` global rule). Since capacitor runs `resize: none`, this manual lift — driven by
 * keyboardWillShow at the START of the animation — keeps the composer in sync with the keyboard
 * instead of snapping late. Used on `<ion-footer kbFollow>`.
 *
 * The transform is applied ONLY while the keyboard is open. A `transform` (even `translateY(0)`)
 * promotes the footer to its own iOS compositor layer, which then paints OVER the destination tab bar
 * for the whole leaving-page slide (the "input overlaps the tab bar, pops at animation end" bug). With
 * no keyboard, we emit no transform at all, so the footer is a plain element that slides out with its
 * page. (`will-change` is likewise omitted in styles.scss for the same reason.) Nothing hides the
 * footer on leave — a page RESETS it imperatively (see the pages' ionViewWillLeave), which is
 * swipe-back-cancel-safe, unlike hiding it.
 */
@Directive({
  selector: '[kbFollow]',
  host: {
    // The class (position:relative + transition + the ::after fill) AND the transform apply ONLY while
    // the keyboard is open. Closed → a completely vanilla <ion-footer> with no positioning/transform/
    // compositor layer, so it slides out with its page on a transition instead of hanging over the
    // destination tab bar.
    '[class.kb-follow]': 'kb.height() > 0',
    '[style.transform]': "kb.height() ? 'translateY(-' + kb.height() + 'px)' : null",
    '[style.--kb-fill.px]': 'kb.height()',
  },
})
export class KbFollowDirective {
  protected readonly kb = inject(KeyboardService);
}
