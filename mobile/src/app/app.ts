import { Component, ChangeDetectionStrategy } from '@angular/core';
import { IonApp, IonRouterOutlet } from '@ionic/angular/standalone';
import { ConnectionPillComponent } from './shared/connection-pill';
import { ApprovalOverlayComponent } from './features/approvals/approval-overlay';

@Component({
  selector: 'app-root',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonApp, IonRouterOutlet, ConnectionPillComponent, ApprovalOverlayComponent],
  template: `
    <ion-app>
      <ion-router-outlet />
      <!-- Global overlays: float over every route, not just the tab shell. -->
      <app-connection-pill />
      <app-approval-overlay />
    </ion-app>
  `,
})
export class App {}
