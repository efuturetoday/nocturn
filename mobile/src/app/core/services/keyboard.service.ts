import { Injectable, signal } from '@angular/core';
import { Capacitor } from '@capacitor/core';
import { Keyboard } from '@capacitor/keyboard';

/**
 * KeyboardService drives keyboard-follow manually (capacitor `resize: none`).
 *
 * Manually, because no resize mode can do it: the plugin applies EVERY mode
 * (`native`/`body`/`ionic`) on a delay of the keyboard's own animation duration plus 200ms
 * (@capacitor/keyboard ios/Sources/KeyboardPlugin/Keyboard.m, setKeyboardHeight:delay:), and applies
 * it as an unanimated frame or height write. Whatever the mode, the composer arrives after the keys
 * have stopped moving, in one jump. `keyboardWillShow` fires at the START of that animation and
 * carries the real `keyboardHeight`, so a CSS transition off it lands in sync.
 *
 * The state is published TWICE, and the difference matters:
 *
 * - as signals, for anything Angular renders from it;
 * - as `--kb-height` and a `kb-open` class on the document element, for the CSS that moves the
 *   composer. That is not a shortcut around Angular. A page LEAVING during a route transition is
 *   OnPush and stops being change-detected, so a binding on it freezes mid-keyboard and the old
 *   chat.page had to strip the transform by hand in `ionViewWillLeave`. A root-level custom property
 *   is not change-detected either — it simply updates — so the leaving page keeps following the
 *   keyboard down and that teardown is not needed.
 *
 * `kb-open` is removed on keyboardDID Hide, not WILL hide: dropping it at the start of the descent
 * would take the CSS transition with it and the composer would snap to rest while the keys were
 * still on their way down.
 */
@Injectable({ providedIn: 'root' })
export class KeyboardService {
  private readonly _open = signal(false);
  /** True while the soft keyboard is showing. */
  readonly open = this._open.asReadonly();

  private readonly _height = signal(0);
  /** Keyboard height in px (0 when closed) — how far to lift the composer + pad the content. */
  readonly height = this._height.asReadonly();

  constructor() {
    if (Capacitor.isNativePlatform()) {
      void Keyboard.addListener('keyboardWillShow', (info) => this.show(info.keyboardHeight));
      void Keyboard.addListener('keyboardWillHide', () => this.hide());
      // The class outlives the descent so the transition survives it; the height goes to 0 at once
      // so the composer starts moving with the keys.
      void Keyboard.addListener('keyboardDidHide', () => this.settle());
    } else {
      // The browser has no willShow; Ionic's DID events are the only signal, and there is no native
      // keyboard animation to be in sync with anyway.
      window.addEventListener('ionKeyboardDidShow', (e) => this.show((e as CustomEvent).detail?.keyboardHeight ?? 0));
      window.addEventListener('ionKeyboardDidHide', () => {
        this.hide();
        this.settle();
      });
    }
  }

  private show(height: number): void {
    this._height.set(height);
    this._open.set(true);
    const root = document.documentElement;
    root.style.setProperty('--kb-height', `${height}px`);
    root.classList.add('kb-open');
  }

  private hide(): void {
    this._height.set(0);
    this._open.set(false);
    document.documentElement.style.setProperty('--kb-height', '0px');
  }

  private settle(): void {
    document.documentElement.classList.remove('kb-open');
  }

  /** Dismiss the keyboard (e.g. a swipe on the message list). On the web there is no soft keyboard,
   * so blur the focused control instead. */
  dismiss(): void {
    if (Capacitor.isNativePlatform()) void Keyboard.hide();
    else (document.activeElement as HTMLElement | null)?.blur();
  }
}
