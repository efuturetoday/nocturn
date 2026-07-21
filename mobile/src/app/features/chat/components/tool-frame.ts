import { Component, ChangeDetectionStrategy, input, inject, computed, signal, effect } from '@angular/core';
import { IonIcon, IonAccordion, IonAccordionGroup, IonItem } from '@ionic/angular/standalone';
import { addIcons } from 'ionicons';
import { checkmarkCircle, alertCircle, ellipsisHorizontalCircle } from 'ionicons/icons';
import { ChatService, type ToolView } from '../../../core/services/chat.service';
import { highlightCode } from '../../../shared/highlight';

/**
 * One tool call in the observable forest (Claude-Code style) rendered as an ACCORDION: a full-width,
 * finger-sized header — icon + name + a compact args preview (e.g. `GET google.de`) + a live duration
 * timer — that expands IN PLACE to the syntax-highlighted input (args / code) and output
 * (result / error). No modal: a big tap target expands inline, so a fat finger never has to pick one
 * tiny row out of the forest. Live (running/err) or from a snapshot (done).
 */
@Component({
  selector: 'app-tool-frame',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [IonIcon, IonAccordion, IonAccordionGroup, IonItem],
  host: { class: 'tool-frame' },
  template: `
    <ion-accordion-group (ionChange)="onToggle($event)">
      <ion-accordion [value]="tool().key">
        <ion-item slot="header" lines="none" class="head">
          <ion-icon [name]="icon()" [color]="color()" slot="start" aria-hidden="true" />
          <div class="head-main">
            <span class="tool-name">{{ tool().tool }}</span>
            @if (argsPreview()) {
              <span class="args">{{ argsPreview() }}</span>
            }
          </div>
          @if (waiting()) {
            <span slot="end" class="wait">needs approval</span>
          } @else if (durationLabel()) {
            <span slot="end" class="dur">{{ durationLabel() }}</span>
          }
        </ion-item>

        <div slot="content" class="detail">
          <section>
            <span class="io-label">input</span>
            @if (inputHtml()) {
              <pre><code [innerHTML]="inputHtml()"></code></pre>
            } @else {
              <pre>{{ inputSource().text }}</pre>
            }
          </section>
          @if (outputSource().text) {
            <section>
              <span class="io-label">output</span>
              @if (outputHtml()) {
                <pre [class.err]="outputSource().err"><code [innerHTML]="outputHtml()"></code></pre>
              } @else {
                <pre [class.err]="outputSource().err">{{ outputSource().text }}</pre>
              }
            </section>
          }
        </div>
      </ion-accordion>
    </ion-accordion-group>
  `,
  styles: `
    :host { display: block; font-size: 0.8rem; }
    ion-accordion-group { background: none; }
    ion-accordion { background: none; }
    .head {
      --background: transparent; --min-height: 2.5rem;
      --padding-start: 0; --inner-padding-end: 0.25rem; --padding-top: 0; --padding-bottom: 0;
      font-size: 0.8rem;
    }
    .head ion-icon { margin-inline-end: 0.375rem; font-size: 1rem; }
    .head-main { display: flex; align-items: center; gap: 0.375rem; min-width: 0; overflow: hidden; }
    .tool-name { font-family: var(--ion-font-family-monospace, monospace); flex-shrink: 0; }
    .args {
      font-family: var(--ion-font-family-monospace, monospace); color: var(--ion-color-medium);
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0;
    }
    .dur { color: var(--ion-color-medium); font-variant-numeric: tabular-nums; flex-shrink: 0; font-size: 0.78rem; }
    .wait { color: var(--ion-color-warning); white-space: nowrap; flex-shrink: 0; font-size: 0.78rem; }

    .detail { padding: 0.25rem 0 0.5rem; }
    .detail section { margin-bottom: 0.75rem; }
    .detail section:last-child { margin-bottom: 0; }
    .io-label {
      display: block; font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.03em;
      color: var(--ion-color-medium); margin-bottom: 0.375rem;
    }
    .detail pre {
      margin: 0; padding: 0.625rem; border-radius: 0.5rem; background: var(--ion-background-color-step-150);
      font-size: 0.78rem; line-height: 1.45; white-space: pre-wrap; word-break: break-word; overflow-x: auto;
    }
    .detail pre.err { color: var(--ion-color-danger); }
    .detail code { font-family: var(--ion-font-family-monospace, ui-monospace, monospace); }
  `,
})
export class ToolFrameComponent {
  readonly tool = input.required<ToolView>();
  private readonly chat = inject(ChatService);

  // Highlight only once the accordion is expanded (lazy — hljs loads on first expand).
  private readonly expanded = signal(false);

  // The LABEL „needs approval" sits on the EXACT tool the daemon named (approval.request.frame) — no
  // guessing which one is waiting.
  protected readonly waiting = computed(() => {
    const p = this.chat.pendingApproval();
    return p?.frame != null && p.frame === this.tool().id;
  });

  // The TIMER freezes for the whole PARKED BRANCH: the waiting tool plus its ancestors (e.g. the
  // code_run around a nested http_read is suspended on that child), which the service walks up the
  // parentId chain. A parallel sibling branch, still executing, keeps ticking.
  private readonly frozen = computed(() => {
    const id = this.tool().id;
    return id != null && this.chat.parkedToolIds().has(id);
  });

  // Ticks while the call runs so the duration counts up live; frozen once it ends (or parked).
  private readonly now = signal(Date.now());
  protected readonly inputHtml = signal('');
  protected readonly outputHtml = signal('');

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

  protected readonly durationLabel = computed(() => {
    const t = this.tool();
    // Running (and this branch not parked on an approval): tick live from the start stamp. Otherwise
    // the daemon's exact durationMs (set on end, restored from a snapshot). Neither → no timing.
    if (t.running && !this.frozen() && t.startedAt != null) {
      return fmtMs(this.now() - t.startedAt);
    }
    return t.durationMs != null ? fmtMs(t.durationMs) : '';
  });

  protected readonly argsPreview = computed(() => preview(this.tool().args));

  // Input: pull a code field out of the args (code_run et al) and show it as code; else pretty JSON.
  protected readonly inputSource = computed<Src>(() => {
    const args = this.tool().args ?? '';
    try {
      const o = JSON.parse(args) as Record<string, unknown>;
      if (o && typeof o === 'object') {
        const codeKey = ['code', 'script', 'source'].find((k) => typeof o[k] === 'string');
        if (codeKey) return { text: o[codeKey] as string, lang: 'javascript' };
        return { text: JSON.stringify(o, null, 2), lang: 'json' };
      }
    } catch {
      /* not JSON */
    }
    return { text: args, lang: undefined };
  });

  protected readonly outputSource = computed<Src & { err: boolean }>(() => {
    const t = this.tool();
    const out = t.err ?? t.result ?? '';
    try {
      return { text: JSON.stringify(JSON.parse(out), null, 2), lang: 'json', err: !!t.err };
    } catch {
      return { text: out, lang: undefined, err: !!t.err };
    }
  });

  constructor() {
    addIcons({ checkmarkCircle, alertCircle, ellipsisHorizontalCircle });

    // Live timer while actually executing — frozen while this branch is parked on an approval (frozen()).
    effect((onCleanup) => {
      if (!this.tool().running || this.frozen()) return;
      const h = setInterval(() => this.now.set(Date.now()), 200);
      onCleanup(() => clearInterval(h));
    });

    // Highlight input/output only once expanded (lazy — hljs loads on first expand).
    effect(() => {
      if (!this.expanded()) return;
      const inp = this.inputSource();
      void highlightCode(inp.text, inp.lang).then((h) => this.inputHtml.set(h));
      const out = this.outputSource();
      if (out.text) void highlightCode(out.text, out.lang).then((h) => this.outputHtml.set(h));
      else this.outputHtml.set('');
    });
  }

  // The group emits its open accordion's value (undefined when collapsed) — flip expanded for our one.
  protected onToggle(ev: CustomEvent<{ value?: string | string[] }>): void {
    this.expanded.set(ev.detail.value === this.tool().key);
  }
}

/** Format a millisecond duration compactly: `340ms` under a second, else `1.2s`. */
function fmtMs(ms: number): string {
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`;
}

interface Src {
  text: string;
  lang?: string;
}

/** A compact one-line args summary for the row — the string/number values joined (e.g. `GET
 * google.de`), else the raw string, truncated. */
function preview(args?: string): string {
  if (!args) return '';
  let s = args;
  try {
    const o = JSON.parse(args);
    if (o && typeof o === 'object') {
      s = Object.values(o)
        .filter((v) => typeof v === 'string' || typeof v === 'number')
        .join(' ');
    }
  } catch {
    /* not JSON — use the raw string */
  }
  s = s.replace(/\s+/g, ' ').trim();
  return s.length > 60 ? s.slice(0, 60) + '…' : s;
}
