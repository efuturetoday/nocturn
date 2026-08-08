import { ApplicationConfig, provideBrowserGlobalErrorListeners, provideAppInitializer, inject } from '@angular/core';
import { provideRouter, withComponentInputBinding, withPreloading, PreloadAllModules } from '@angular/router';
import { provideIonicAngular } from '@ionic/angular/standalone';
import { provideLucideConfig } from '@lucide/angular';
import { Capacitor } from '@capacitor/core';
import { StatusBar, Style } from '@capacitor/status-bar';

import { routes } from './app.routes';
import { ToastService } from './core/services/toast.service';
import { KeyboardService } from './core/services/keyboard.service';
import { JoinPromptService } from './core/services/join-prompt.service';
import { PresenceService } from './core/services/presence.service';
import { PushService } from './core/services/push.service';
import { NotificationService } from './core/services/notification.service';

export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    // withComponentInputBinding: route params/query/data bind straight to component input()s.
    // Preloading matters for the FEEL, not just the speed: the hero → chat hand-off plays a custom
    // transition, and a lazy chunk fetched at the moment of navigation delays its first frame by a
    // few hundred ms. The screen sits still, then moves — which reads as a broken animation rather
    // than as a slow one. Fetching the routes in idle time after the first paint removes the stall.
    provideRouter(routes, withComponentInputBinding(), withPreloading(PreloadAllModules)),
    // scrollPadding OFF. With `resize: none` Ionic switches on a fallback that pads the focused
    // ion-content by its ESTIMATED keyboard height — a 290px default, not the real one — and clears
    // it on a 120ms timeout after focusout (@ionic/core utils/input-shims/hacks/scroll-padding.js).
    // On the hero that padding shrinks the content box and shoves the centred lockup upward: a
    // layout jump nobody wrote, on a page whose whole requirement is that the keyboard just overlays
    // it. scrollAssist stays ON — that is the separate hack stopping WebKit from relocating the
    // focused input and dragging the page with it.
    provideIonicAngular({ scrollPadding: false }),
    // Lucide's default stroke of 2 is heavier than the ionicons weight the rest of the UI was drawn
    // against; 1.75 sits at the same optical weight at 24px. Set once here so no icon has to carry
    // the value at its call site.
    provideLucideConfig({ strokeWidth: 1.75 }),
    // Eagerly construct the always-on listeners at startup (their constructors wire the effects:
    // error toasts, keyboard-follow, and the auto pairing-request overlay) + native chrome setup.
    provideAppInitializer(() => {
      inject(ToastService);
      inject(KeyboardService);
      inject(JoinPromptService);
      inject(PresenceService);
      // Explicit, not left to PushService's transitive injection: the in-app delivery of a fired
      // reminder must work on every platform, including where there is no push at all.
      inject(NotificationService);
      inject(PushService); // registers the APNs token once connected (native only)
      if (Capacitor.isNativePlatform()) {
        // Immersive: webview draws under the status bar (Ionic headers still offset via safe-area).
        // Style.Dark = light icons/text, for the dark nocturn background.
        void StatusBar.setOverlaysWebView({ overlay: true });
        void StatusBar.setStyle({ style: Style.Dark });
      }
    }),
  ],
};
