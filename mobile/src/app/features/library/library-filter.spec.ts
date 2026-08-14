import { describe, it, expect } from 'vitest';
import { filterCatalog, hostOf } from './library-filter';
import type { LibraryCatalog } from '../../core/protocol/nocturn-protocol';

const CATALOG: LibraryCatalog = {
  type: 'library.catalog',
  version: 'test',
  skills: [
    {
      id: 'commit-messages',
      title: 'Commit messages',
      description: 'Writes commit messages that say why, not what.',
      tags: ['git', 'writing'],
      body: '---\nname: commit-messages\n---\n\nA body mentioning Linear, which no query should reach.',
    },
    { id: 'travel', title: 'Travel planning', description: 'Plans a trip.', body: '# Travel' },
  ],
  plugins: [
    {
      id: 'gmail',
      title: 'Gmail (read-only)',
      description: 'Search and read your mail.',
      tags: ['mail'],
      name: 'gmail',
      tools: ['gmail_search', 'gmail_read'],
      uses: ['http_read'],
      hosts: ['gmail.googleapis.com'],
      scopes: ['https://www.googleapis.com/auth/gmail.readonly'],
      manifest: '{"name":"gmail"}',
      script: '// a script mentioning Linear, which no query should reach',
    },
  ],
  mcp: [
    {
      id: 'linear',
      title: 'Linear',
      description: 'Issues, projects and cycles.',
      tags: ['work'],
      name: 'linear',
      url: 'https://mcp.linear.app/sse',
      auth: 'oauth',
      scopes: ['read'],
    },
    { id: 'weather', title: 'Weather', description: 'Forecasts.', name: 'weather', url: 'https://weather.example/mcp' },
  ],
};

const ids = (kind: 'all' | 'skill' | 'plugin' | 'mcp', q = ''): string[] =>
  filterCatalog(CATALOG, q, kind).map((e) => e.id);

describe('filterCatalog', () => {
  it('shows every kind under all, in catalog order', () => {
    expect(ids('all')).toEqual(['commit-messages', 'travel', 'gmail', 'linear', 'weather']);
  });

  it('narrows to one kind, and the others are gone rather than dimmed', () => {
    expect(ids('skill')).toEqual(['commit-messages', 'travel']);
    expect(ids('plugin')).toEqual(['gmail']);
    expect(ids('mcp')).toEqual(['linear', 'weather']);
  });

  // A daemon older than the plugin channel sends no `plugins` at all, and so does a catalog that
  // offers none. Both mean "no plugins"; neither may throw on a screen somebody just opened.
  it('survives a catalog with no plugins field', () => {
    const old = { ...CATALOG, plugins: undefined } as unknown as LibraryCatalog;
    expect(filterCatalog(old, '', 'all').map((e) => e.id)).toEqual(['commit-messages', 'travel', 'linear', 'weather']);
    expect(filterCatalog(old, '', 'plugin')).toEqual([]);
  });

  it('matches the title regardless of case', () => {
    expect(ids('all', 'TRAVEL')).toEqual(['travel']);
  });

  it('matches the description and the tags, not only the title', () => {
    expect(ids('all', 'forecasts')).toEqual(['weather']);
    expect(ids('all', 'git')).toEqual(['commit-messages']);
  });

  it('never matches a skill body or a plugin script — both would match everything', () => {
    // "Linear" appears in the commit-messages BODY, in the gmail plugin's SCRIPT, and in the Linear
    // server's title. Only the title is a match.
    expect(ids('all', 'linear')).toEqual(['linear']);
  });

  it('trims the query, so a stray space is not a filter', () => {
    expect(ids('all', '   ')).toEqual(ids('all'));
  });

  it('returns nothing rather than throwing before a catalog has arrived', () => {
    expect(filterCatalog(null, 'anything', 'all')).toEqual([]);
  });

  it('gives each kind the subtitle that identifies it', () => {
    const [skill] = filterCatalog(CATALOG, 'travel', 'all');
    const [plugin] = filterCatalog(CATALOG, 'gmail', 'all');
    const [server] = filterCatalog(CATALOG, 'linear', 'all');

    expect(skill.sub).toBe('');
    expect(plugin.sub).toBe('2 tools');
    expect(server.sub).toBe('mcp.linear.app');
  });
});

describe('hostOf', () => {
  it('falls back to the whole URL when it will not parse', () => {
    expect(hostOf('not a url')).toBe('not a url');
  });
});
