import { Component, ChangeDetectionStrategy, ViewEncapsulation, input, computed } from '@angular/core';
import { marked } from 'marked';

/**
 * Markdown config. `image` is neutered to a link (never an <img>) so chat content can't trigger
 * an egress fetch from the device — the exfil vector Nocturn defends against. Raw HTML in the
 * model output is NOT injected: Angular's built-in sanitizer strips scripts / on*-handlers /
 * javascript: URLs when the result is bound via [innerHTML].
 */
marked.use({
  gfm: true,
  breaks: true,
  renderer: {
    image(token) {
      return `<a href="${token.href}" class="md-img">🖼 ${escapeHtml(token.text || 'image')}</a>`;
    },
  },
});

/** Minimal HTML escape for text we splice into renderer output. */
function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function odd(count: number): boolean {
  return count % 2 === 1;
}
function occurrences(s: string, sub: string): number {
  return s.split(sub).length - 1;
}

/**
 * Auto-close dangling inline markers so emphasis/code formats WHILE streaming — before the real
 * closing marker arrives. Incomplete block constructs (tables) are left alone: marked defers them
 * until complete on its own. Heuristic; worst case a marker formats a beat late, never corrupts.
 */
export function completeStreaming(src: string): string {
  let s = src;

  // Open fenced code block → close it (renders as a live code block); its body is literal, so
  // do NOT touch inline markers inside it.
  if (odd(occurrences(s, '```'))) return s + '\n```';

  // Inline code span.
  if (odd((s.match(/`/g) || []).length)) s += '`';

  // Strong before emphasis to avoid mis-nesting.
  if (odd(occurrences(s, '**'))) s += '**';
  const stars = s.replace(/\*\*/g, '').match(/\*/g); // single * (excluding ** pairs)
  if (stars && odd(stars.length)) s += '*';

  if (odd(occurrences(s, '__'))) s += '__';
  const unders = s.replace(/__/g, '').match(/_/g);
  if (unders && odd(unders.length)) s += '_';

  if (odd(occurrences(s, '~~'))) s += '~~';

  return s;
}

/** Render markdown → HTML string (streaming-safe). Bind via [innerHTML] so Angular sanitizes. */
export function renderMarkdown(src: string): string {
  const html = marked.parse(completeStreaming(src), { async: false }) as string;
  // Open links out-of-app, never as a same-origin/prefetched navigation.
  return html.replace(/<a href/g, '<a target="_blank" rel="noopener noreferrer" href');
}

/** Renders a (possibly still-streaming) markdown string. Used for assistant chat content. */
@Component({
  selector: 'app-markdown',
  changeDetection: ChangeDetectionStrategy.OnPush,
  // None: the rendered markdown is injected via [innerHTML], so those nodes never receive the
  // component's _ngcontent attribute — Emulated encapsulation would leave every `.md …` rule
  // unmatched (the whole stylesheet dead). Every selector below is `.md`-scoped, which keeps the
  // styles namespaced to this component's output even without Angular's attribute scoping.
  encapsulation: ViewEncapsulation.None,
  template: `<div class="md" [innerHTML]="html()"></div>`,
  styles: `
    .md { display: block; font-size: 1rem; line-height: 1.55; }
    .md > :first-child { margin-top: 0; }
    .md > :last-child { margin-bottom: 0; }

    .md p { margin: 0 0 0.5rem; }

    .md h1, .md h2, .md h3, .md h4, .md h5, .md h6 {
      margin: 1rem 0 0.375rem;
      line-height: 1.25;
      font-weight: 650;
    }
    .md h1 { font-size: 1.3rem; }
    .md h2 { font-size: 1.15rem; }
    .md h3 { font-size: 1.03rem; }
    .md h4, .md h5, .md h6 { font-size: 1rem; }
    .md h4 { color: var(--ion-color-medium); }

    .md ul, .md ol { margin: 0 0 0.5rem; padding-left: 1.35rem; }
    .md li { margin: 0.125rem 0; }
    .md li::marker { color: var(--ion-color-medium); }

    .md a { color: var(--ion-color-primary); text-underline-offset: 2px; }
    .md strong { font-weight: 650; }
    .md hr { border: none; border-top: 1px solid var(--ion-background-color-step-200); margin: 0.875rem 0; }

    .md code {
      font-family: var(--ion-font-family-monospace, ui-monospace, monospace);
      font-size: 0.85em;
      background: var(--ion-background-color-step-200);
      padding: 0.0625rem 0.3125rem;
      border-radius: 0.3125rem;
    }
    .md pre {
      background: var(--ion-background-color-step-150);
      padding: 0.625rem 0.75rem;
      border-radius: 0.625rem;
      overflow-x: auto;
      margin: 0 0 0.5rem;
    }
    .md pre code { background: none; padding: 0; font-size: 0.82em; }

    .md blockquote {
      margin: 0 0 0.5rem;
      padding-left: 0.75rem;
      border-left: 2px solid var(--ion-color-primary);
      color: var(--ion-color-medium);
    }

    .md .table-wrap, .md table { display: block; overflow-x: auto; }
    .md table { border-collapse: collapse; margin: 0 0 0.5rem; font-size: 0.9em; }
    .md th, .md td { border: 1px solid var(--ion-background-color-step-250); padding: 0.25rem 0.5rem; text-align: left; }
    .md th { background: var(--ion-background-color-step-150); }
    .md .md-img { color: var(--ion-color-primary); }
  `,
})
export class MarkdownComponent {
  readonly text = input.required<string>();
  protected readonly html = computed(() => renderMarkdown(this.text()));
}
