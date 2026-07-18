import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import { IonApp, IonRouterOutlet } from '@ionic/angular/standalone';
import { ToastService } from './core/services/toast.service';

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
  // Instantiate the toast listener at startup (server errors + connection drops).
  private readonly toast = inject(ToastService);
}
