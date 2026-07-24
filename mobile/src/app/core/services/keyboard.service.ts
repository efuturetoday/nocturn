import { Injectable, signal } from '@angular/core';
import { Capacitor } from '@capacitor/core';
import { Keyboard } from '@capacitor/keyboard';

/**
 * KeyboardService drives keyboard-follow manually (capacitor resize: none). `keyboardWillShow` fires
 * at the START of the iOS keyboard animation and carries `keyboardHeight`, so the footer can slide up
 * IN SYNC via a CSS transition ([kbFollow]) instead of snapping after the animation finishes (the
 * resize-mode late-snap). `open` drives the tab-bar hide (tabs.page).
 */
@Injectable({ providedIn: 'root' })
export class KeyboardService {
  private readonly _open = signal(false);
  /** True while the soft keyboard is showing. Drives the tab-bar hide. */
  readonly open = this._open.asReadonly();

  private readonly _height = signal(0);
  /** Keyboard height in px (0 when closed) — how far to lift the footer + pad the content. */
  readonly height = this._height.asReadonly();

  constructor() {
    if (Capacitor.isNativePlatform()) {
      void Keyboard.addListener('keyboardWillShow', (info) => {
        this._height.set(info.keyboardHeight);
        this._open.set(true);
      });
      void Keyboard.addListener('keyboardWillHide', () => {
        this._height.set(0);
        this._open.set(false);
      });
    } else {
      window.addEventListener('ionKeyboardDidShow', (e) => {
        this._height.set((e as CustomEvent).detail?.keyboardHeight ?? 0);
        this._open.set(true);
      });
      window.addEventListener('ionKeyboardDidHide', () => {
        this._height.set(0);
        this._open.set(false);
      });
    }
  }

  /** Dismiss the keyboard (e.g. a swipe on the message list). On the web there is no soft keyboard,
   * so blur the focused control instead. */
  dismiss(): void {
    if (Capacitor.isNativePlatform()) void Keyboard.hide();
    else (document.activeElement as HTMLElement | null)?.blur();
  }
}
