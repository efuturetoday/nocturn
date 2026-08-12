import type { LibraryCatalog, LibrarySkill, LibraryServer } from '../../core/protocol/nocturn-protocol';

/** Which part of the catalog is on screen. `all` mixes both kinds, so every card names its own. */
export type LibraryKind = 'all' | 'skill' | 'mcp';

/** One catalog entry, flattened so one grid holds both kinds. `item` carries the original back for
    the detail sheet, which does differ by kind. */
export interface LibraryEntry {
  kind: 'skill' | 'mcp';
  id: string;
  title: string;
  description: string;
  tags: string[];
  /** The third line on a card: a server's host, or nothing for a skill. */
  sub: string;
  item: LibrarySkill | LibraryServer;
}

/**
 * Flatten and filter the catalog.
 *
 * The query matches title, description and tags — NOT the skill body: a body is thousands of tokens
 * and would match nearly everything. Order is the catalog's, skills before servers; re-sorting by
 * relevance would reshuffle the grid under the cursor while typing.
 */
export function filterCatalog(catalog: LibraryCatalog | null, query: string, kind: LibraryKind): LibraryEntry[] {
  if (!catalog) return [];
  const q = query.trim().toLowerCase();
  const out: LibraryEntry[] = [];

  if (kind !== 'mcp') {
    for (const s of catalog.skills) {
      const e: LibraryEntry = {
        kind: 'skill',
        id: s.id,
        title: s.title,
        description: s.description,
        tags: s.tags ?? [],
        sub: '',
        item: s,
      };
      if (matches(e, q)) out.push(e);
    }
  }

  if (kind !== 'skill') {
    for (const m of catalog.mcp) {
      const e: LibraryEntry = {
        kind: 'mcp',
        id: m.id,
        title: m.title,
        description: m.description,
        tags: m.tags ?? [],
        sub: hostOf(m.url),
        item: m,
      };
      if (matches(e, q)) out.push(e);
    }
  }

  return out;
}

function matches(e: LibraryEntry, q: string): boolean {
  if (!q) return true;
  return [e.title, e.description, ...e.tags].some((s) => s.toLowerCase().includes(q));
}

/** The host, for the card's third line. A URL that will not parse is shown whole. */
export function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}
