import { Component, ChangeDetectionStrategy, input, computed } from '@angular/core';
import { IonIcon, IonNote } from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { checkmarkCircle, alertCircle, ellipsisHorizontalCircle } from 'ionicons/icons';
import type { ToolView } from '../../../core/services/chat.service';

/** One tool call in the observable forest — dim; live (running/err) or from snapshot (done). */
@Component({
  selector: 'app-tool-frame',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonIcon, IonNote],
  host: { class: 'tool-frame' },
  template: `
    <ion-icon [name]="icon()" [color]="color()" aria-hidden="true" />
    <span class="tool-name">{{ tool().tool }}</span>
    @if (tool().err) {
      <ion-note color="danger">{{ tool().err }}</ion-note>
    } @else if (tool().running) {
      <ion-note color="medium">running…</ion-note>
    }
  `,
  styles: `
    :host {
      display: flex;
      align-items: center;
      gap: 6px;
      font-size: 0.8rem;
      padding: 2px 0;
    }
    .tool-name { font-family: var(--ion-font-family-monospace, monospace); }
  `,
})
export class ToolFrameComponent {
  readonly tool = input.required<ToolView>();

  protected readonly icon = computed(() => {
    const t = this.tool();
    if (t.err) return 'alert-circle';
    return t.running ? 'ellipsis-horizontal-circle' : 'checkmark-circle';
  });
  protected readonly color = computed(() => {
    const t = this.tool();
    if (t.err) return 'danger';
    return t.running ? 'medium' : 'success';
  });

  constructor() {
    addIcons({ checkmarkCircle, alertCircle, ellipsisHorizontalCircle });
  }
}
