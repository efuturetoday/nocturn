import { describe, it, expect } from 'vitest';

/**
 * Breakpoints belong to Ionic. A hand-written width query is a second scale, and no token can
 * prevent it: a custom property is unreadable inside `@media`, and component styles are inline
 * strings rather than SCSS. So the rule is held here instead — see styles.scss for what to use.
 *
 * `prefers-reduced-motion` is untouched: a preference, not a width. Sources come from Vite's raw
 * glob because the runner has no filesystem.
 */

const WIDTH_QUERY = /@media[^{]*\((?:min|max)-width/;

/** Every source file under src/app, as text. Eager so the assertion is a plain loop. */
const sources = import.meta.glob('./**/*.ts', { query: '?raw', import: 'default', eager: true }) as Record<
  string,
  string
>;

describe('the layout invariant', () => {
  it('reads the whole of src/app, so an empty sweep cannot pass for a clean one', () => {
    // A scanner that silently matches nothing is the failure mode this test exists to avoid, so the
    // corpus is asserted before what is in it.
    expect(Object.keys(sources).length).toBeGreaterThan(20);
    expect(Object.keys(sources).some((p) => p.endsWith('features/shell/shell.page.ts'))).toBe(true);
  });

  it('can still see a width media query when there is one', () => {
    const fixture = '  styles: `@media (min-width: 600px) { .card { display: none; } }`,';

    expect(WIDTH_QUERY.test(fixture)).toBe(true);
  });

  it('leaves prefers-reduced-motion alone — a preference is not a width', () => {
    expect(WIDTH_QUERY.test('@media (prefers-reduced-motion: reduce) { * { animation: none; } }')).toBe(false);
  });

  it('finds no width media query anywhere in src/app', () => {
    const offenders = Object.entries(sources)
      .filter(([path]) => !path.endsWith('layout-invariant.spec.ts'))
      .filter(([, text]) => WIDTH_QUERY.test(text))
      .map(([path]) => path);

    expect(
      offenders,
      'Breakpoints come from Ionic: use ion-col size-sm/md/lg/xl for columns, ion-hide-*-up|down to ' +
        'show and hide, or a min()/clamp() for something fluid. See the note beside --nocturn-measure ' +
        'in src/styles.scss.',
    ).toEqual([]);
  });
});
