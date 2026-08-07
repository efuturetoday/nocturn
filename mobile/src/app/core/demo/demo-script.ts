/**
 * The scripted turn — the one thing in the demo that has to be convincing, because it is the app's
 * whole argument: a turn runs, it reaches for an effect it does not yet have permission for, and it
 * parks until a human on this device answers.
 *
 * A turn is expressed as timed steps rather than as a single blob, so it STREAMS: tokens arrive a
 * few tens of milliseconds apart, a tool frame opens before it closes and ticks its own timer while
 * open. The daemon schedules them; nothing here touches a timer or the DOM.
 *
 * The tool ids matter. `frame` on a `chat.tool` is the ENCLOSING call (0 = top level) and `id` is
 * the call itself, so `http_read` at frame 1 nests under `code_run` — the host-bridge nesting a real
 * run produces. `approval.request.frame` then names the http_write call by ITS id, which is what
 * freezes that frame's timer and paints the parked branch.
 */

import type { ServerEvent } from '../protocol/nocturn-protocol';

/** One scheduled event: `after` ms from the previous step. */
export interface Step {
  after: number;
  event: ServerEvent;
}

/** Gap between streamed tokens — slow enough to read as typing, fast enough not to test patience. */
const TOKEN_MS = 22;
const THINKING_MS = 32;

const CODE_RUN = 1;
const HTTP_READ = 2;
const HTTP_WRITE = 3;

/**
 * Everything up to and including the approval request. The turn then hangs — deliberately — until
 * `resumeSteps` runs, which is the only honest way to show that the effect really is parked.
 */
export function openingSteps(chatId: string, input: string, approvalId: string): Step[] {
  const steps: Step[] = [{ after: 60, event: { type: 'chat.turnStart', chatId, frame: 0 } }];

  push(steps, THINKING_MS, chunks(thinkingFor(input)), (text) => ({ type: 'chat.thinking', chatId, frame: 0, text }));

  steps.push({
    after: 140,
    event: { type: 'chat.tool', chatId, phase: 'start', frame: 0, id: CODE_RUN, tool: 'code_run', args: JSON.stringify({ language: 'js' }) },
  });
  steps.push({
    after: 90,
    event: {
      type: 'chat.tool',
      chatId,
      phase: 'start',
      frame: CODE_RUN, // nested: the script reached back through the host bridge
      id: HTTP_READ,
      tool: 'http_read',
      args: JSON.stringify({ url: 'https://api.github.com/repos/nocturn/nocturn/issues?state=open' }),
    },
  });
  steps.push({
    after: 480,
    event: {
      type: 'chat.tool',
      chatId,
      phase: 'end',
      frame: CODE_RUN,
      id: HTTP_READ,
      tool: 'http_read',
      args: JSON.stringify({ url: 'https://api.github.com/repos/nocturn/nocturn/issues?state=open' }),
      result: JSON.stringify({ open: 9, matching: 0 }),
      durationMs: 476,
    },
  });
  steps.push({
    after: 120,
    event: {
      type: 'chat.tool',
      chatId,
      phase: 'end',
      frame: 0,
      id: CODE_RUN,
      tool: 'code_run',
      args: JSON.stringify({ language: 'js' }),
      result: JSON.stringify({ matching: 0 }),
      durationMs: 604,
    },
  });

  push(steps, TOKEN_MS, chunks(OPENING_TEXT), (text) => ({ type: 'chat.token', chatId, frame: 0, text }));

  steps.push({
    after: 220,
    event: {
      type: 'chat.tool',
      chatId,
      phase: 'start',
      frame: 0,
      id: HTTP_WRITE,
      tool: 'http_write',
      args: JSON.stringify({ url: 'https://api.github.com/repos/nocturn/nocturn/issues', method: 'POST' }),
    },
  });
  steps.push({
    after: 260,
    event: {
      type: 'approval.request',
      id: approvalId,
      frame: HTTP_WRITE,
      chatId,
      kind: 'net',
      target: 'api.github.com',
      // The ids are the broker's own vocabulary (internal/hitl/broker.go). DENY_OPTION is never
      // offered — the sheet mints the refusal itself, so nothing the daemon sends can approve.
      options: [
        { id: 'once', recall: 'never' },
        { id: 'session', recall: 'session' },
        { id: 'always', recall: 'always' },
        { id: 'widen0', recall: 'always', widen: { kind: 'net', target: '*.github.com' } },
      ],
    },
  });

  return steps;
}

/**
 * The rest of the turn, once the human has answered. Both branches exist because both will be
 * taken: a reviewer is at least as likely to press Deny as to hold an Allow, and a demo that only
 * survives approval would misrepresent what the gate does with a refusal.
 */
export function resumeSteps(chatId: string, approved: boolean): Step[] {
  const args = JSON.stringify({ url: 'https://api.github.com/repos/nocturn/nocturn/issues', method: 'POST' });
  const steps: Step[] = [
    {
      after: 320,
      event: approved
        ? {
            type: 'chat.tool',
            chatId,
            phase: 'end',
            frame: 0,
            id: HTTP_WRITE,
            tool: 'http_write',
            args,
            result: JSON.stringify({ number: 118, state: 'open' }),
            durationMs: 508,
          }
        : {
            type: 'chat.tool',
            chatId,
            phase: 'end',
            frame: 0,
            id: HTTP_WRITE,
            tool: 'http_write',
            args,
            err: 'denied by the human',
            durationMs: 11,
          },
    },
  ];

  push(steps, TOKEN_MS, chunks(approved ? ALLOWED_TEXT : DENIED_TEXT), (text) => ({ type: 'chat.token', chatId, frame: 0, text }));
  steps.push({ after: 120, event: { type: 'chat.turnEnd', chatId, frame: 0, tokens: approved ? 1284 : 1131 } });
  return steps;
}

// ── the prose ────────────────────────────────────────────────────────────────

const OPENING_TEXT = [
  'I read the open issues first — nine of them, none covering this.',
  '',
  'So the next step is to file one. That is a write to a host you have not granted yet, so it is not',
  'mine to make:',
].join('\n');

const ALLOWED_TEXT = [
  '',
  '',
  'Filed as **#118**, with the context from this chat in the body. Nothing else needed writing.',
].join('\n');

const DENIED_TEXT = [
  '',
  '',
  'Understood — nothing was written, and the grant was not remembered. The draft is still here if you',
  'want to take it somewhere else.',
].join('\n');

/** A little reasoning that acknowledges what was actually typed, so the turn is not visibly canned. */
function thinkingFor(input: string): string {
  const asked = input.trim().replace(/\s+/g, ' ').slice(0, 60) || 'the request';
  return `The ask: "${asked}". Check what is already on record before writing anything back.`;
}

// ── plumbing ─────────────────────────────────────────────────────────────────

/** Split text into token-sized pieces, keeping the whitespace attached like a real tokenizer does. */
function chunks(text: string): string[] {
  return text.match(/\s*\S+|\s+/g) ?? [];
}

/** Append one step per chunk, each `after` ms behind the last. */
function push(steps: Step[], after: number, parts: string[], build: (text: string) => ServerEvent): void {
  for (const text of parts) steps.push({ after, event: build(text) });
}
