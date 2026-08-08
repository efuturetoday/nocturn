import { CapacitorConfig } from '@capacitor/cli';
import { KeyboardResize, KeyboardStyle } from '@capacitor/keyboard';

const config: CapacitorConfig = {
  appId: 'me.itexpert.nocturn',
  appName: 'Nocturn',
  webDir: 'dist/mobile/browser',
  plugins: {
    // resize: none. Not a preference — no mode can do what is needed here. The plugin applies every
    // one of native/body/ionic on a delay of the keyboard's animation duration PLUS 200ms, as an
    // unanimated write (Keyboard.m, setKeyboardHeight:delay:), so the composer always lands after the
    // keys have stopped. native additionally resizes the WebView, which changes what a vh is and
    // shrinks the hero's mascot; body and ionic shrink ion-app, which moves the hero's centred
    // lockup. So: nothing resizes, and KeyboardService lifts the composer from keyboardWillShow.
    //
    // Dark keyboard, always. Otherwise iOS takes the keyboard's look from the DEVICE appearance and
    // a phone in light mode drops a white keyboard under a dark-only app — theme/variables.css has
    // no light variant to fall back to.
    Keyboard: { resize: KeyboardResize.None, style: KeyboardStyle.Dark },
  },
  server: {
    url: 'http://192.168.2.179:4200'
  }
};

export default config;
