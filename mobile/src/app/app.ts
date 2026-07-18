import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import { IonApp, IonRouterOutlet } from '@ionic/angular/standalone';
import { Capacitor } from '@capacitor/core';
import { StatusBar, Style } from '@capacitor/status-bar';
import { ToastService } from './core/services/toast.service';
import { KeyboardService } from './core/services/keyboard.service';

@Component({
  selector: 'app-root',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonApp, IonRouterOutlet],
  template: `
    <ion-app>
      <ion-router-outlet />
    </ion-app>
  `,
})
export class App {
  // Instantiate the toast + keyboard listeners at startup.
  private readonly toast = inject(ToastService);
  private readonly keyboard = inject(KeyboardService);

  constructor() {
    if (Capacitor.isNativePlatform()) {
      // Immersive: let the webview draw under the status bar (Ionic headers still offset via
      // safe-area). Style.Dark = light icons/text, for our dark nocturn background.
      void StatusBar.setOverlaysWebView({ overlay: true });
      void StatusBar.setStyle({ style: Style.Dark });
    }
  }
}
