// Lazy syntax highlighting via highlight.js — the core + a handful of languages are dynamically
// imported on first use, so they stay OUT of the initial bundle (only loaded when a tool detail is
// opened). The output is hljs-* class spans, coloured by the global .hljs-* rules in styles.scss.
import type { HLJSApi, LanguageFn } from 'highlight.js';

let hljsPromise: Promise<HLJSApi> | null = null;

async function core(): Promise<HLJSApi> {
  hljsPromise ??= (async () => {
    const hljs = (await import('highlight.js/lib/core')).default;
    const langs: Record<string, Promise<{ default: LanguageFn }>> = {
      javascript: import('highlight.js/lib/languages/javascript'),
      typescript: import('highlight.js/lib/languages/typescript'),
      json: import('highlight.js/lib/languages/json'),
      bash: import('highlight.js/lib/languages/bash'),
      python: import('highlight.js/lib/languages/python'),
      go: import('highlight.js/lib/languages/go'),
    };
    for (const [name, mod] of Object.entries(langs)) {
      hljs.registerLanguage(name, (await mod).default);
    }
    return hljs;
  })();
  return hljsPromise;
}

/** Highlight code → HTML (escaped, with hljs-* spans). Uses `lang` when known, else auto-detects. */
export async function highlightCode(code: string, lang?: string): Promise<string> {
  const hljs = await core();
  if (lang && hljs.getLanguage(lang)) return hljs.highlight(code, { language: lang }).value;
  return hljs.highlightAuto(code).value;
}
