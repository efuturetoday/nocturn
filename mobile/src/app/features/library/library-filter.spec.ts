import { describe, it, expect } from 'vitest';
import { filterCatalog, hostOf } from './library-filter';
import type { LibraryCatalog } from '../../core/protocol/nocturn-protocol';

const CATALOG: LibraryCatalog = {
  type: 'library.catalog',
  version: 'test',
  entries: [
    {
      id: 'commit-messages',
      title: 'Commit messages',
      description: 'Writes commit messages that say why, not what.',
      tags: ['git', 'writing'],
      carries: ['skill'],
      skill: '---\nname: commit-messages\n---\n\nA body mentioning Linear, which no query should reach.',
    },
    {
      id: 'travel',
      title: 'Travel planning',
      description: 'Plans a trip.',
      carries: ['skill'],
      skill: '# Travel',
    },
    {
      id: 'gmail',
      title: 'Gmail (read-only)',
      description: 'Search and read your mail.',
      tags: ['mail'],
      // One entry carrying two payloads: the code and the instructions for using it.
      carries: ['plugin', 'skill'],
      tools: ['gmail_search', 'gmail_read'],
      uses: ['http_read'],
      hosts: ['gmail.googleapis.com'],
      scopes: ['https://www.googleapis.com/auth/gmail.readonly'],
      script: '// a script mentioning Linear, which no query should reach',
    },
    {
      id: 'linear',
      title: 'Linear',
      description: 'Issues, projects and cycles.',
      tags: ['work'],
      carries: ['mcp'],
      url: 'https://mcp.linear.app/sse',
      scopes: ['read'],
    },
    {
      id: 'house',
      title: 'Home Assistant',
      description: 'Control the house.',
      carries: ['skill'],
      settings: [{ name: 'base_url', type: 'url', label: 'Address of your server' }],
      skill: '# House',
    },
  ],
};

const ids = (kind: 'all' | 'skill' | 'plugin' | 'mcp', q = ''): string[] =>
  filterCatalog(CATALOG, q, kind).map((c) => c.entry.id);

describe('filterCatalog', () => {
  it('shows everything under all, in catalog order', () => {
    expect(ids('all')).toEqual(['commit-messages', 'travel', 'gmail', 'linear', 'house']);
  });

  // The filter is by what an entry CARRIES, not by a kind of entry — so one that brings code and
  // instructions belongs under both, rather than having to pick a side.
  it('narrows by payload, and an entry carrying several appears under each', () => {
    expect(ids('skill')).toEqual(['commit-messages', 'travel', 'gmail', 'house']);
    expect(ids('plugin')).toEqual(['gmail']);
    expect(ids('mcp')).toEqual(['linear']);
  });

  // A daemon that sends no entries at all, and a catalog that offers none, are both "nothing here";
  // neither may throw on a screen somebody just opened.
  it('survives a catalog with no entries field', () => {
    const empty = { ...CATALOG, entries: undefined } as unknown as LibraryCatalog;
    expect(filterCatalog(empty, '', 'all')).toEqual([]);
  });

  it('matches the title regardless of case', () => {
    expect(ids('all', 'TRAVEL')).toEqual(['travel']);
  });

  it('matches the description and the tags, not only the title', () => {
    expect(ids('all', 'issues')).toEqual(['linear']);
    expect(ids('all', 'git')).toEqual(['commit-messages']);
  });

  it('never matches a skill body or a plugin script — both would match everything', () => {
    // "Linear" appears in the commit-messages BODY, in the gmail entry's SCRIPT, and in the Linear
    // server's title. Only the title is a match.
    expect(ids('all', 'linear')).toEqual(['linear']);
  });

  it('trims the query, so a stray space is not a filter', () => {
    expect(ids('all', '   ')).toEqual(ids('all'));
  });

  it('returns nothing rather than throwing before a catalog has arrived', () => {
    expect(filterCatalog(null, 'anything', 'all')).toEqual([]);
  });

  // The third line is the most concrete thing about an entry — and a setting comes before the rest,
  // because "needs an address" decides whether installing finishes the job or starts a second task.
  it('gives each entry the subtitle that identifies it', () => {
    const sub = (q: string) => filterCatalog(CATALOG, q, 'all')[0].sub;

    expect(sub('travel')).toBe('');
    expect(sub('gmail')).toBe('2 tools');
    expect(sub('linear')).toBe('mcp.linear.app');
    expect(sub('home assistant')).toBe('needs base_url');
  });
});

describe('hostOf', () => {
  it('falls back to the whole URL when it will not parse', () => {
    expect(hostOf('not a url')).toBe('not a url');
  });
});
