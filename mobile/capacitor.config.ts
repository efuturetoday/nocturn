import { CapacitorConfig } from '@capacitor/cli';
import { KeyboardResize } from '@capacitor/keyboard';

const config: CapacitorConfig = {
  appId: 'me.itexpert.nocturn',
  appName: 'Nocturn',
  webDir: 'dist/mobile/browser',
  server: {
    url: 'http://192.168.2.179:4200',
  },
  plugins: {
    // resize: none — the resize modes (native/ionic/body) only adjust layout AFTER the iOS keyboard
    // animation finishes, so the footer snaps up late + the scroll jumps. Instead we don't resize and
    // lift the footer ourselves, in sync with keyboardWillShow (KeyboardService.height + [kbFollow]).
    Keyboard: { resize: KeyboardResize.None },
  },
};

export default config;
