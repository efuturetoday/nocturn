import { describe, it, expect } from 'vitest';
import type { Message, ToolNode, ServerEvent, ChatSnapshot } from '../protocol/nocturn-protocol';
import {
  EMPTY,
  seed,
  applyEvent,
  pushUser,
  buildSnapshotMessages,
  buildForestTools,
  type ChatView,
} from './chat-model';

// Fold a live event sequence from a start state (default EMPTY) — the same path the service drives.
function fold(events: ServerEvent[], from: ChatView = EMPTY, now = 1000): ChatView {
  return events.reduce((v, e) => applyEvent(v, e, now), from);
}

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
  const nodes: ToolNode[] = [
    { id: 1, parent: 0, tool: 'code_run', args: '{}' },
    { id: 2, parent: 1, tool: 'http_read', args: '{}', durationMs: 42 },
  ];

  it('uses snapshot `s` keys, is never running, restores depth + duration', () => {
    const tools = buildForestTools(nodes);
    expect(tools.map((t) => t.key)).toEqual(['s1', 's2']);
    expect(tools.every((t) => t.running === false)).toBe(true);
    expect(tools.map((t) => t.depth)).toEqual([0, 1]);
    expect(tools[1].durationMs).toBe(42);
  });
});

describe('seed', () => {
  it('replays the running turn (input + events) onto the transcript with live-keyed tools', () => {
    const snap: ChatSnapshot = {
      type: 'chat.snapshot',
      id: 'c1',
      messages: [
        { role: 'user', content: 'earlier' },
        { role: 'assistant', content: 'reply' },
      ],
      tools: [[], []],
      inflightRunning: true,
      inflightInput: 'now',
      inflightEvents: [
        { type: 'chat.turnStart', chatId: 'c1', frame: 0 },
        { type: 'chat.thinking', chatId: 'c1', frame: 0, text: 'hmm' },
        { type: 'chat.token', chatId: 'c1', frame: 0, text: 'partial' },
        { type: 'chat.tool', chatId: 'c1', phase: 'start', frame: 0, id: 5, tool: 'http_read', args: '{}' },
      ],
    };
    const v = seed(snap, 1000);
    expect(v.running).toBe(true);
    expect(v.messages.map((m) => m.role)).toEqual(['user', 'assistant', 'user', 'assistant']);
    const pending = v.messages[3];
    expect(pending).toMatchObject({ content: 'partial', thinking: 'hmm', pending: true });
    expect(pending.tools[0]).toMatchObject({ key: 'l5', running: true }); // live-keyed so a live ToolEnd updates it
  });

  it('shows a pending assistant bubble in the Submit→turnStart window (input, no events yet)', () => {
    const snap: ChatSnapshot = {
      type: 'chat.snapshot',
      id: 'c1',
      messages: [],
      tools: [],
      inflightRunning: true,
      inflightInput: 'just sent',
    };
    const v = seed(snap, 1000);
    expect(v.running).toBe(true);
    expect(v.messages.map((m) => [m.role, m.pending])).toEqual([
      ['user', false],
      ['assistant', true], // opened even before the first event streams, so the composer shows running
    ]);
  });

  it('is idle with no running turn', () => {
    const snap: ChatSnapshot = { type: 'chat.snapshot', id: 'c1', messages: [], tools: [] };
    expect(seed(snap, 1000)).toEqual(EMPTY);
  });
});

describe('applyEvent (live fold)', () => {
  it('opens the bubble on turnStart, streams token + thinking, ends the turn', () => {
    const v = fold([
      { type: 'chat.turnStart', chatId: 'c', frame: 0 },
      { type: 'chat.token', chatId: 'c', frame: 0, text: 'hel' },
      { type: 'chat.token', chatId: 'c', frame: 0, text: 'lo' },
      { type: 'chat.thinking', chatId: 'c', frame: 0, text: 'think' },
      { type: 'chat.turnEnd', chatId: 'c', frame: 0, tokens: 3 },
    ]);
    expect(v.running).toBe(false);
    expect(v.messages).toHaveLength(1);
    expect(v.messages[0]).toMatchObject({ role: 'assistant', content: 'hello', thinking: 'think', pending: false });
  });

  it('nests a tool by its enclosing frame and freezes duration on end', () => {
    const v = fold([
      { type: 'chat.turnStart', chatId: 'c', frame: 0 },
      { type: 'chat.tool', chatId: 'c', phase: 'start', frame: 0, id: 1, tool: 'code_run', args: '{}' },
      { type: 'chat.tool', chatId: 'c', phase: 'start', frame: 1, id: 2, tool: 'http_read', args: '{}' },
      { type: 'chat.tool', chatId: 'c', phase: 'end', frame: 1, id: 2, tool: 'http_read', args: '{}', result: 'ok', durationMs: 12 },
    ]);
    const tools = v.messages[0].tools;
    expect(tools.map((t) => [t.key, t.depth, t.parentId])).toEqual([
      ['l1', 0, undefined],
      ['l2', 1, 1],
    ]);
    expect(tools[1]).toMatchObject({ running: false, durationMs: 12 });
    expect(tools[0].running).toBe(true); // still open
  });

  it('ignores events for non-frame-0 answer text and unknown types', () => {
    const v = fold([
      { type: 'chat.turnStart', chatId: 'c', frame: 0 },
      { type: 'chat.token', chatId: 'c', frame: 1, text: 'nested' }, // sub-agent text — not the main bubble
    ]);
    expect(v.messages[0].content).toBe('');
  });
});

// The whole point of the unification: a live sequence and the snapshot of its end state must reduce
// to the SAME rendered messages — pinning the two paths together so they cannot drift.
describe('convergence: live fold vs snapshot seed', () => {
  it('a completed turn folds equal to its persisted snapshot', () => {
    // Live: user submits, one tool round, then the answer.
    const live = pushUser(EMPTY, 'q');
    const streamed = fold(
      [
        { type: 'chat.turnStart', chatId: 'c', frame: 0 },
        { type: 'chat.tool', chatId: 'c', phase: 'start', frame: 0, id: 1, tool: 'http_read', args: '{}' },
        { type: 'chat.tool', chatId: 'c', phase: 'end', frame: 0, id: 1, tool: 'http_read', args: '{}', result: 'ok', durationMs: 7 },
        { type: 'chat.token', chatId: 'c', frame: 0, text: 'answer' },
        { type: 'chat.turnEnd', chatId: 'c', frame: 0, tokens: 2 },
      ],
      live,
    );

    // Snapshot: the same turn as the daemon persists it (transcript + per-turn forest, no inflight).
    const snap: ChatSnapshot = {
      type: 'chat.snapshot',
      id: 'c',
      messages: [
        { role: 'user', content: 'q' },
        { role: 'assistant', content: '', toolCalls: [{ id: 'http_read', tool: 'http_read', args: '{}' }] },
        { role: 'tool', content: 'ok', toolCallID: 'http_read' },
        { role: 'assistant', content: 'answer' },
      ],
      tools: [[{ id: 1, parent: 0, tool: 'http_read', args: '{}', result: 'ok', durationMs: 7 }]],
    };
    const seeded = seed(snap, 0);

    // Keys differ by namespace (live `l` vs snapshot `s`) and live tools carry startedAt — normalize
    // those away; everything that renders (roles, text, nesting, durations) must match.
    const norm = (v: ChatView) =>
      v.messages.map((m) => ({
        role: m.role,
        content: m.content,
        pending: m.pending,
        tools: m.tools.map((t) => ({ tool: t.tool, depth: t.depth, parentId: t.parentId, durationMs: t.durationMs, running: t.running })),
      }));

    expect(norm(streamed)).toEqual(norm(seeded));
    expect(streamed.running).toBe(seeded.running); // both idle at turn end
  });
});
