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
  return (
    html
      // Open links out-of-app, never as a same-origin/prefetched navigation.
      .replace(/<a href/g, '<a target="_blank" rel="noopener noreferrer" href')
      // Wrap tables in a horizontal scroller — a wide table scrolls inside its own box instead of
      // squishing every column to per-character wrapping (or forcing the whole page wide).
      .replace(/<table>/g, '<div class="table-wrap"><table>')
      .replace(/<\/table>/g, '</table></div>')
  );
}

/** Renders a (possibly still-streaming) markdown string. Used for assistant chat content. */
@Component({
  selector: 'app-markdown',
  changeDetection: ChangeDetectionStrategy.OnPush,
  // None: the rendered markdown is injected via [innerHTML], so those nodes never receive the
  // component's _ngcontent attribute — Emulated encapsulation would leave every rule unmatched
  // (the whole stylesheet dead). So these styles are GLOBAL; the container class must therefore be
  // app-unique. `.md` is NOT unique — it is Ionic's platform MODE class, stamped on every ion-*
  // component (ion-tab-bar.md, ion-tab-button.md, …). A global `.md { display: block }` overrode
  // Ionic's `:host { display: flex }` and broke the tab bar (buttons stacked vertically). Hence
  // `.markdown`, which nothing else uses.
  encapsulation: ViewEncapsulation.None,
  template: `<div class="markdown" [innerHTML]="html()"></div>`,
  styles: `
    .markdown { display: block; font-size: 1rem; line-height: 1.55; }
    .markdown > :first-child { margin-top: 0; }
    .markdown > :last-child { margin-bottom: 0; }

    .markdown p { margin: 0 0 0.5rem; }

    .markdown h1, .markdown h2, .markdown h3, .markdown h4, .markdown h5, .markdown h6 {
      margin: 1rem 0 0.375rem;
      line-height: 1.25;
      font-weight: 650;
    }
    .markdown h1 { font-size: 1.3rem; }
    .markdown h2 { font-size: 1.15rem; }
    .markdown h3 { font-size: 1.03rem; }
    .markdown h4, .markdown h5, .markdown h6 { font-size: 1rem; }
    .markdown h4 { color: var(--ion-color-medium); }

    .markdown ul, .markdown ol { margin: 0 0 0.5rem; padding-left: 1.35rem; }
    .markdown li { margin: 0.125rem 0; }
    .markdown li::marker { color: var(--ion-color-medium); }

    .markdown a { color: var(--ion-color-primary); text-underline-offset: 2px; }
    .markdown strong { font-weight: 650; }
    .markdown hr { border: none; border-top: 1px solid var(--ion-background-color-step-200); margin: 0.875rem 0; }

    .markdown code {
      font-family: var(--ion-font-family-monospace, ui-monospace, monospace);
      font-size: 0.85em;
      background: var(--ion-background-color-step-200);
      padding: 0.0625rem 0.3125rem;
      border-radius: 0.3125rem;
    }
    .markdown pre {
      background: var(--ion-background-color-step-150);
      padding: 0.625rem 0.75rem;
      border-radius: 0.625rem;
      overflow-x: auto;
      margin: 0 0 0.5rem;
    }
    .markdown pre code { background: none; padding: 0; font-size: 0.82em; }

    .markdown blockquote {
      margin: 0 0 0.5rem;
      padding-left: 0.75rem;
      border-left: 2px solid var(--ion-color-primary);
      color: var(--ion-color-medium);
    }

    .markdown .table-wrap { overflow-x: auto; max-width: 100%; margin: 0 0 0.5rem; -webkit-overflow-scrolling: touch; }
    .markdown table { border-collapse: collapse; font-size: 0.9em; }
    .markdown th, .markdown td {
      border: 1px solid var(--ion-background-color-step-250); padding: 0.25rem 0.5rem; text-align: left;
      white-space: nowrap; /* let the table exceed its box and scroll, rather than squish columns */
    }
    .markdown th { background: var(--ion-background-color-step-150); }
    .markdown .md-img { color: var(--ion-color-primary); }
  `,
})
export class MarkdownComponent {
  readonly text = input.required<string>();
  protected readonly html = computed(() => renderMarkdown(this.text()));
}
