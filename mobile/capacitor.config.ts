import { CapacitorConfig } from '@capacitor/cli';
import { KeyboardResize } from '@capacitor/keyboard';

const config: CapacitorConfig = {
  appId: 'me.itexpert.nocturn',
  appName: 'mobile',
  webDir: 'dist/mobile/browser',
  server: {
    url: 'http://192.168.2.179:4200',
  },
  plugins: {
    // resize: none — every resize mode (native/body/ionic) adjusts the layout only AFTER the iOS
    // keyboard animation finishes (the well-known "footer snaps late" bug). Instead we don't resize
    // at all and move the footer ourselves in sync with keyboardWillShow (see KeyboardService).
    Keyboard: { resize: KeyboardResize.None },
  },
};

export default config;
