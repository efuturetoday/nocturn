import type { LibraryCatalog, LibrarySkill, LibraryServer, LibraryPlugin } from '../../core/protocol/nocturn-protocol';

/** Which part of the catalog is on screen. `all` mixes the kinds, so every card names its own. */
export type LibraryKind = 'all' | 'skill' | 'plugin' | 'mcp';

/** One catalog entry, flattened so one grid holds every kind. `item` carries the original back for
    the detail sheet, which does differ by kind. */
export interface LibraryEntry {
  kind: 'skill' | 'plugin' | 'mcp';
  id: string;
  title: string;
  description: string;
  tags: string[];
  /** The third line on a card: a server's host, a plugin's tool count, nothing for a skill. */
  sub: string;
  item: LibrarySkill | LibraryServer | LibraryPlugin;
}

/**
 * Flatten and filter the catalog.
 *
 * The query matches title, description and tags — NOT a skill body or a plugin's source: both are
 * thousands of tokens and would match nearly everything. Order is the catalog's, kind by kind;
 * re-sorting by relevance would reshuffle the grid under the cursor while typing.
 */
export function filterCatalog(catalog: LibraryCatalog | null, query: string, kind: LibraryKind): LibraryEntry[] {
  if (!catalog) return [];
  const q = query.trim().toLowerCase();
  const out: LibraryEntry[] = [];

  if (kind === 'all' || kind === 'skill') {
    for (const s of catalog.skills) {
      push(out, q, {
        kind: 'skill',
        id: s.id,
        title: s.title,
        description: s.description,
        tags: s.tags ?? [],
        sub: '',
        item: s,
      });
    }
  }

  if (kind === 'all' || kind === 'plugin') {
    // `plugins` is absent from a daemon older than the plugin channel, and from a catalog that
    // offers none — both are "no plugins", neither is an error.
    for (const p of catalog.plugins ?? []) {
      push(out, q, {
        kind: 'plugin',
        id: p.id,
        title: p.title,
        description: p.description,
        tags: p.tags ?? [],
        sub: toolSummary(p),
        item: p,
      });
    }
  }

  if (kind === 'all' || kind === 'mcp') {
    for (const m of catalog.mcp) {
      push(out, q, {
        kind: 'mcp',
        id: m.id,
        title: m.title,
        description: m.description,
        tags: m.tags ?? [],
        sub: hostOf(m.url),
        item: m,
      });
    }
  }

  return out;
}

function push(out: LibraryEntry[], q: string, e: LibraryEntry): void {
  if (matches(e, q)) out.push(e);
}

function matches(e: LibraryEntry, q: string): boolean {
  if (!q) return true;
  return [e.title, e.description, ...e.tags].some((s) => s.toLowerCase().includes(q));
}

/** A plugin's third line: how many tools it adds, which is the thing it is FOR. */
export function toolSummary(p: LibraryPlugin): string {
  const n = p.tools?.length ?? 0;
  return n === 1 ? '1 tool' : `${n} tools`;
}

/** The host, for the card's third line. A URL that will not parse is shown whole. */
export function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}
