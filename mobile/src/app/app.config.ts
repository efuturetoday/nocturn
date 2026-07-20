import { ApplicationConfig, provideBrowserGlobalErrorListeners, provideAppInitializer, inject } from '@angular/core';
import { provideRouter, withComponentInputBinding } from '@angular/router';
import { provideIonicAngular } from '@ionic/angular/standalone';
import { Capacitor } from '@capacitor/core';
import { StatusBar, Style } from '@capacitor/status-bar';

import { routes } from './app.routes';
import { ToastService } from './core/services/toast.service';
import { KeyboardService } from './core/services/keyboard.service';
import { JoinPromptService } from './core/services/join-prompt.service';
import { PresenceService } from './core/services/presence.service';
import { PushService } from './core/services/push.service';

export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    // withComponentInputBinding: route params/query/data bind straight to component input()s.
    provideRouter(routes, withComponentInputBinding()),
    provideIonicAngular({}),
    // Eagerly construct the always-on listeners at startup (their constructors wire the effects:
    // error toasts, keyboard-follow, and the auto pairing-request overlay) + native chrome setup.
    provideAppInitializer(() => {
      inject(ToastService);
      inject(KeyboardService);
      inject(JoinPromptService);
      inject(PresenceService);
      inject(PushService);
      if (Capacitor.isNativePlatform()) {
        // Immersive: webview draws under the status bar (Ionic headers still offset via safe-area).
        // Style.Dark = light icons/text, for the dark nocturn background.
        void StatusBar.setOverlaysWebView({ overlay: true });
        void StatusBar.setStyle({ style: Style.Dark });
      }
    }),
  ],
};
