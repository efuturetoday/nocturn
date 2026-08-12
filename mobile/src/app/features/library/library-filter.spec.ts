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

const ids = (kind: 'all' | 'skill' | 'mcp', q = ''): string[] => filterCatalog(CATALOG, q, kind).map((e) => e.id);

describe('filterCatalog', () => {
  it('shows both kinds under all, skills first', () => {
    expect(ids('all')).toEqual(['commit-messages', 'travel', 'linear', 'weather']);
  });

  it('narrows to one kind, and the other is gone rather than dimmed', () => {
    expect(ids('skill')).toEqual(['commit-messages', 'travel']);
    expect(ids('mcp')).toEqual(['linear', 'weather']);
  });

  it('matches the title regardless of case', () => {
    expect(ids('all', 'TRAVEL')).toEqual(['travel']);
  });

  it('matches the description and the tags, not only the title', () => {
    expect(ids('all', 'forecasts')).toEqual(['weather']);
    expect(ids('all', 'git')).toEqual(['commit-messages']);
  });

  it('never matches a skill body — thousands of tokens would match everything', () => {
    // "Linear" appears in the commit-messages BODY and in the Linear server's title.
    expect(ids('all', 'linear')).toEqual(['linear']);
  });

  it('trims the query, so a stray space is not a filter', () => {
    expect(ids('all', '   ')).toEqual(ids('all'));
  });

  it('returns nothing rather than throwing before a catalog has arrived', () => {
    expect(filterCatalog(null, 'anything', 'all')).toEqual([]);
  });

  it('carries a server host as the card subtitle and leaves a skill without one', () => {
    const [skill] = filterCatalog(CATALOG, 'travel', 'all');
    const [server] = filterCatalog(CATALOG, 'linear', 'all');

    expect(skill.sub).toBe('');
    expect(server.sub).toBe('mcp.linear.app');
  });
});

describe('hostOf', () => {
  it('falls back to the whole URL when it will not parse', () => {
    expect(hostOf('not a url')).toBe('not a url');
  });
});
