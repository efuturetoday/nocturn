import { Injectable, signal } from '@angular/core';
import { Capacitor } from '@capacitor/core';
import { Keyboard } from '@capacitor/keyboard';

/**
 * KeyboardService drives keyboard-follow manually (resize: none). `keyboardWillShow` fires at the
 * START of the iOS keyboard animation and carries `keyboardHeight`, so the chat footer can slide up
 * IN SYNC with the keyboard (a CSS transition matches the iOS curve) instead of snapping after it
 * finishes — the known iOS resize-timing bug. `open` also drives the tab-bar hide.
 */
@Injectable({ providedIn: 'root' })
export class KeyboardService {
  private readonly _open = signal(false);
  readonly open = this._open.asReadonly();

  private readonly _height = signal(0);
  /** Keyboard height in px (0 when closed) — how far to lift the footer. */
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
}
