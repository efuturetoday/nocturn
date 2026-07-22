import { describe, it, expect } from 'vitest';
import type { Message, ToolNode } from '../protocol/nocturn-protocol';
import { buildForestTools, buildSnapshotMessages } from './chat-snapshot';

describe('buildSnapshotMessages', () => {
  it('merges a multi-message assistant turn into ONE bubble', () => {
    // assistant(tool_calls) · tool(result) · assistant(text) — one rendered turn.
    const messages: Message[] = [
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: '', toolCalls: [{ id: 'a', tool: 'http_read', args: '{}' }] },
      { role: 'tool', content: 'ok', toolCallID: 'a' },
      { role: 'assistant', content: 'done' },
    ];
    const forest: ToolNode[][] = [[{ id: 1, parent: 0, tool: 'http_read', args: '{}', result: 'ok' }]];

    const out = buildSnapshotMessages(messages, forest);

    expect(out).toHaveLength(2); // user + one merged assistant bubble
    expect(out[0]).toMatchObject({ role: 'user', content: 'hi' });
    expect(out[1].role).toBe('assistant');
    expect(out[1].content).toBe('done'); // empty first content, then the text round
    expect(out[1].tools).toHaveLength(1); // tools come from the forest group, not the flat toolCalls
    expect(out[1].tools[0]).toMatchObject({ key: 's1', tool: 'http_read', depth: 0 });
  });

  it('joins text across assistant rounds of the same turn', () => {
    const messages: Message[] = [
      { role: 'user', content: 'q' },
      { role: 'assistant', content: 'first' },
      { role: 'assistant', content: 'second' },
    ];
    const out = buildSnapshotMessages(messages, [[]]);
    expect(out[1].content).toBe('first\nsecond');
  });

  it('restores nested depth + parentId from the forest group', () => {
    const messages: Message[] = [
      { role: 'user', content: 'go' },
      { role: 'assistant', content: 'ok' },
    ];
    // code_run (id 1) around a nested http_read (id 2, parent 1).
    const forest: ToolNode[][] = [
      [
        { id: 1, parent: 0, tool: 'code_run', args: '{}' },
        { id: 2, parent: 1, tool: 'http_read', args: '{}' },
      ],
    ];
    const tools = buildSnapshotMessages(messages, forest)[1].tools;
    expect(tools.map((t) => [t.tool, t.depth, t.parentId])).toEqual([
      ['code_run', 0, undefined],
      ['http_read', 1, 1],
    ]);
  });

  it('gives a turn with no forest group empty tools', () => {
    const messages: Message[] = [
      { role: 'user', content: 'q' },
      { role: 'assistant', content: 'a' },
    ];
    const out = buildSnapshotMessages(messages, []); // no forest at all
    expect(out[1].tools).toEqual([]);
  });

  it('skips tool + system messages as bubbles', () => {
    const messages: Message[] = [
      { role: 'system', content: 'persona' },
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: 'yo' },
    ];
    const out = buildSnapshotMessages(messages, [[]]);
    expect(out.map((m) => m.role)).toEqual(['user', 'assistant']);
  });
});

describe('buildForestTools', () => {
  const nodes: Array<ToolNode & { running?: boolean }> = [
    { id: 1, parent: 0, tool: 'code_run', args: '{}', running: true },
    { id: 2, parent: 1, tool: 'http_read', args: '{}', durationMs: 42 },
  ];

  it('uses snapshot `s` keys and is never running when live=false', () => {
    const tools = buildForestTools(nodes);
    expect(tools.map((t) => t.key)).toEqual(['s1', 's2']);
    expect(tools.every((t) => t.running === false)).toBe(true);
    expect(tools[1].durationMs).toBe(42);
  });

  it('uses live `l` keys and honours the running flag when live=true', () => {
    const tools = buildForestTools(nodes, true);
    expect(tools.map((t) => t.key)).toEqual(['l1', 'l2']);
    expect(tools[0].running).toBe(true);
    expect(tools[1].running).toBe(false);
  });
});
