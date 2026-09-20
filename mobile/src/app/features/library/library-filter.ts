import type { LibraryCatalog, LibraryEntry, LibraryPayload } from '../../core/protocol/nocturn-protocol';

/** Which part of the catalog is on screen. `all` mixes everything, so every card names what it
    carries. The filter is by PAYLOAD, not by kind of entry: an integration that brings a server and
    the instructions for it belongs under both. */
export type LibraryKind = 'all' | LibraryPayload;

/** One catalog entry as the grid renders it: the daemon's entry plus the two lines a card needs. */
export interface LibraryCard {
  entry: LibraryEntry;
  /** The third line: a server's host, a plugin's tool count, the settings a skill needs. */
  sub: string;
}

/**
 * Filter the catalog.
 *
 * The query matches title, description and tags — NOT a skill body or a plugin's source: both are
 * thousands of tokens and would match nearly everything. Order is the catalog's; re-sorting by
 * relevance would reshuffle the grid under the cursor while typing.
 */
export function filterCatalog(catalog: LibraryCatalog | null, query: string, kind: LibraryKind): LibraryCard[] {
  if (!catalog) return [];
  const q = query.trim().toLowerCase();
  const out: LibraryCard[] = [];

  for (const entry of catalog.entries ?? []) {
    if (kind !== 'all' && !carries(entry, kind)) continue;
    if (!matches(entry, q)) continue;
    out.push({ entry, sub: subtitle(entry) });
  }
  return out;
}

/** Whether an entry brings the given payload. */
export function carries(e: LibraryEntry, payload: LibraryPayload): boolean {
  return (e.carries ?? []).includes(payload);
}

function matches(e: LibraryEntry, q: string): boolean {
  if (!q) return true;
  return [e.title, e.description, ...(e.tags ?? [])].some((s) => s.toLowerCase().includes(q));
}

/**
 * The card's third line — the most concrete thing about this entry.
 *
 * A server's host and a plugin's tool count are what those are FOR. A setting comes before both,
 * because "needs an address" is what decides whether installing finishes the job or starts a second
 * task.
 */
export function subtitle(e: LibraryEntry): string {
  const settings = e.settings ?? [];
  if (settings.length > 0) {
    return settings.length === 1 ? `needs ${settings[0].name}` : `needs ${settings.length} settings`;
  }
  if (carries(e, 'mcp') && e.url) return hostOf(e.url);
  if (carries(e, 'plugin')) return toolSummary(e);
  return '';
}

/** A plugin's line: how many tools it adds, which is the thing it is FOR. */
export function toolSummary(e: LibraryEntry): string {
  const n = e.tools?.length ?? 0;
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
