/**
 * The scripted daemon behind the demo. It answers `ClientCommand`s with `ServerEvent`s and nothing
 * else — no Angular, no DOM, no timers of its own — so the whole app's state can be driven from it
 * and it can be tested as a plain function of commands.
 *
 * It is deliberately a DAEMON and not a mock UI: the app's own reducers (`chat-model.ts`,
 * ApprovalService, ChatListService, …) render what comes out of here, over the same wire types the
 * real daemon uses. A demo cannot therefore show a screen the real app could not produce, and a
 * protocol change breaks the demo's test rather than the demo silently.
 *
 * Two details are load-bearing:
 *
 *   • **`join.list` answers with an EMPTY list.** `JoinPromptService` auto-presents a modal for any
 *     pending join, which would put a sheet in front of a reviewer that they cannot dismiss.
 *   • **A turn is streamed, then parked at the approval.** The turn's remaining steps run only when
 *     `approval.resolve` arrives, on the allow branch or the deny branch — which is the one thing
 *     the app exists to demonstrate.
 *
 * A running turn is identified by a sequence number per chat, so `chat.cancel` (or a second submit)
 * invalidates the steps still scheduled instead of trying to unschedule them.
 */

import {
  DENY_OPTION,
  type ChatMeta,
  type ClientCommand,
  type ServerEvent,
  type Source,
  type ToolNode,
} from '../protocol/nocturn-protocol';
import { DEMO_WORKSPACE, demoAccounts, demoAgents, demoChats, demoReminders, type DemoChat } from './demo-data';
import { openingSteps, resumeSteps, type Step } from './demo-script';

/** What the daemon needs from its transport: a way out, a clock, and a way to defer. */
export interface DemoHost {
  emit(event: ServerEvent): void;
  schedule(delayMs: number, fn: () => void): void;
  now(): number;
}

/** A turn in flight: enough to hand it over on a mid-turn `chat.open`, and to finish it later. */
interface Turn {
  seq: number;
  input: string;
  events: ServerEvent[]; // exactly what has streamed, replayable by the client's own reducer
  text: string; // the frame-0 tokens so far — becomes the persisted assistant message
  nodes: ToolNode[]; // the finished tool calls — becomes the turn's forest
  approvalId?: string;
}

const WS = DEMO_WORKSPACE.name;

export class DemoDaemon {
  private readonly chats: DemoChat[];
  private reminders;
  private readonly agents = demoAgents();
  private readonly accounts = demoAccounts();

  /** The live turn per chat id. */
  private readonly turns = new Map<string, Turn>();
  private seq = 0;
  private minted = 0;

  constructor(private readonly host: DemoHost) {
    const now = host.now();
    this.chats = demoChats(now);
    this.reminders = demoReminders(now);
  }

  /** Route one command. Unknown commands are ignored — the real daemon answers `error`, but there is
      nothing a reviewer could do with it and a toast would only look like a fault. */
  handle(cmd: ClientCommand): void {
    switch (cmd.cmd) {
      case 'workspace.list':
        this.soon({ type: 'workspace.list', items: [DEMO_WORKSPACE] });
        break;
      case 'chat.list':
        this.soon({ type: 'chat.list', ws: WS, kind: cmd.kind, chats: this.metasOf(cmd.kind) });
        break;
      case 'agent.list':
        this.soon({ type: 'agent.list', ws: WS, agents: this.agents });
        break;
      case 'reminder.list':
        this.soon({ type: 'reminder.list', ws: WS, reminders: this.reminders });
        break;
      case 'auth.list':
        this.soon({ type: 'auth.accounts', ws: WS, accounts: this.accounts });
        break;
      case 'join.list':
        this.soon({ type: 'join.list', joins: [] });
        break;
      case 'chat.open':
        this.open(cmd.id);
        break;
      case 'chat.submit':
        this.submit(cmd.id, cmd.text);
        break;
      case 'chat.cancel':
        this.cancel(cmd.id);
        break;
      case 'chat.markRead':
        this.markRead(cmd.id);
        break;
      case 'agent.fire':
        this.fire(cmd.name, cmd.task);
        break;
      case 'reminder.cancel':
        this.cancelReminder(cmd.id);
        break;
      case 'approval.resolve':
        this.resolve(cmd.id, cmd.option);
        break;
      case 'auth.begin':
        // Never open an external browser during a review — and there is no daemon to hold a token.
        this.soon({ type: 'auth.done', server: cmd.server, ok: false, error: 'Connecting accounts is unavailable in demo mode.' });
        break;
      case 'auth.callback':
      case 'presence.set':
        break; // nothing to report
    }
  }

  // ── chats ──────────────────────────────────────────────────────────────────

  /** Reply with the chat's snapshot. An id the demo has never seen is a freshly minted chat, which
      the client is allowed to open before its first message — it gets an empty transcript. */
  private open(id: string): void {
    const chat = this.chats.find((c) => c.meta.id === id);
    const turn = this.turns.get(id);
    this.soon({
      type: 'chat.snapshot',
      id,
      messages: chat?.messages ?? [],
      tools: chat?.tools ?? [],
      ...(turn ? { inflightRunning: true, inflightInput: turn.input, inflightEvents: turn.events } : {}),
    });
  }

  private submit(id: string, text: string): void {
    if (this.turns.has(id)) return; // a turn is already running in this chat
    this.ensureChat(id, text);

    const approvalId = this.mint('ap');
    const turn: Turn = { seq: ++this.seq, input: text, events: [], text: '', nodes: [], approvalId };
    this.turns.set(id, turn);
    this.run(id, turn, openingSteps(id, text, approvalId));
  }

  /** Resume the parked turn. Anything the daemon did not offer refuses — the same rule the broker
      keeps (`pick()` in internal/hitl), so there is no value here that approves by accident. */
  private resolve(approvalId: string, option: string): void {
    this.soon({ type: 'approval.resolved', id: approvalId });

    const entry = [...this.turns.entries()].find(([, t]) => t.approvalId === approvalId);
    if (!entry) return;
    const [chatId, turn] = entry;
    turn.approvalId = undefined;

    const approved = option !== DENY_OPTION && ['once', 'session', 'always', 'widen0'].includes(option);
    this.run(chatId, turn, resumeSteps(chatId, approved), () => this.finish(chatId, turn));
  }

  private cancel(id: string): void {
    const turn = this.turns.get(id);
    if (!turn) return;
    turn.seq = -1; // invalidates every step still scheduled for it
    this.turns.delete(id);
    if (turn.approvalId) this.soon({ type: 'approval.resolved', id: turn.approvalId });
    this.soon({ type: 'chat.turnEnd', chatId: id, frame: 0, err: 'cancelled', tokens: 0 });
    this.finish(id, turn);
  }

  private markRead(id: string): void {
    const chat = this.chats.find((c) => c.meta.id === id);
    if (!chat || chat.meta.read === chat.meta.updated) return;
    chat.meta = { ...chat.meta, read: chat.meta.updated };
    this.soon({ type: 'chat.activity', ws: WS, chat: chat.meta });
  }

  // ── agents & reminders ─────────────────────────────────────────────────────

  /** Fire an agent: a run is a chat in the AGENT store, created by the daemon and streamed like any
      other. It ends with a `notification`, the in-app half of the wake a real run would push. */
  private fire(name: string, task?: string): void {
    const id = this.mint('ru');
    const now = new Date(this.host.now()).toISOString();
    const input = task ?? 'Run your scheduled task now.';
    const chat: DemoChat = {
      meta: { id, name, source: 'agent', agent: name, created: now, updated: now, read: now, turns: 0, preview: 'Running…' },
      messages: [],
      tools: [],
    };
    this.chats.unshift(chat);
    this.soon({ type: 'chat.activity', ws: WS, chat: chat.meta });

    const turn: Turn = { seq: ++this.seq, input, events: [], text: '', nodes: [] };
    this.turns.set(id, turn);
    this.run(id, turn, agentRunSteps(id, name), () => {
      this.finish(id, turn);
      this.host.emit({ type: 'notification', ws: WS, kind: 'notify', chatId: id, title: name, message: 'The run finished — three items, nothing needing you.' });
    });
  }

  private cancelReminder(id: string): void {
    this.reminders = this.reminders.filter((r) => r.id !== id);
    this.soon({ type: 'reminder.changed', ws: WS });
  }

  // ── running a turn ─────────────────────────────────────────────────────────

  /**
   * Play a step list against a chat, dropping everything if the turn was superseded meanwhile. Each
   * emitted event is also recorded on the turn: the frame-0 tokens become the persisted assistant
   * message and the finished tool calls become its forest, so the snapshot after the turn is exactly
   * what streamed rather than a second, hand-written version of it.
   */
  private run(chatId: string, turn: Turn, steps: Step[], done?: () => void): void {
    const seq = turn.seq;
    let at = 0;
    for (const step of steps) {
      at += step.after;
      this.host.schedule(at, () => {
        if (this.turns.get(chatId)?.seq !== seq) return;
        this.record(turn, step.event);
        this.host.emit(step.event);
      });
    }
    this.host.schedule(at + 1, () => {
      if (this.turns.get(chatId)?.seq !== seq) return;
      done?.();
    });
  }

  private record(turn: Turn, event: ServerEvent): void {
    turn.events.push(event);
    if (event.type === 'chat.token' && event.frame === 0) turn.text += event.text;
    if (event.type === 'chat.tool' && event.phase === 'end') {
      turn.nodes.push({ id: event.id, parent: event.frame, tool: event.tool, args: event.args, result: event.result, err: event.err, durationMs: event.durationMs });
    }
  }

  /**
   * Persist a finished turn into the transcript so reopening the chat shows it. The RUNNING turn is
   * deliberately not in `messages` until here — that is the real daemon's rule (the transcript
   * persists at turn end), and it is what keeps a mid-turn `chat.open` from showing the user's
   * message twice: once from the transcript and once from `inflightInput`.
   */
  private finish(chatId: string, turn: Turn): void {
    this.turns.delete(chatId);
    const chat = this.chats.find((c) => c.meta.id === chatId);
    if (!chat) return;
    chat.messages.push({ role: 'user', content: turn.input }, { role: 'assistant', content: turn.text });
    chat.tools.push(turn.nodes);
    const updated = new Date(this.host.now()).toISOString();
    chat.meta = { ...chat.meta, updated, turns: chat.meta.turns + 1, preview: firstLine(turn.text) };
    this.host.emit({ type: 'chat.activity', ws: WS, chat: chat.meta });
  }

  // ── helpers ────────────────────────────────────────────────────────────────

  /** The chat for an id, creating it when the client minted a fresh one (an unknown id starts a
      chat — the same rule the real daemon keeps for `chat.submit`). */
  private ensureChat(id: string, text: string): DemoChat {
    const existing = this.chats.find((c) => c.meta.id === id);
    if (existing) return existing;
    const now = new Date(this.host.now()).toISOString();
    const chat: DemoChat = {
      meta: { id, name: firstLine(text), source: 'user', created: now, updated: now, read: now, turns: 0, preview: firstLine(text) },
      messages: [],
      tools: [],
    };
    this.chats.unshift(chat);
    this.host.emit({ type: 'chat.activity', ws: WS, chat: chat.meta });
    return chat;
  }

  private metasOf(kind: Source): ChatMeta[] {
    return this.chats
      .filter((c) => c.meta.source === kind)
      .map((c) => c.meta)
      .sort((a, b) => b.updated.localeCompare(a.updated));
  }

  /** An id shaped like the daemon's (lowercase hex), counted rather than random so a test can
      predict it and two runs of the demo look the same. */
  private mint(prefix: string): string {
    return prefix + (++this.minted).toString(16).padStart(10, '0');
  }

  /** Reply on the next tick — a command is never answered synchronously inside `send`. */
  private soon(event: ServerEvent): void {
    this.host.schedule(0, () => this.host.emit(event));
  }
}

/** A short agent run: no approval, because a `guarded` scheduled run that needed one would park and
    the reviewer would have fired something that just sits there. */
function agentRunSteps(chatId: string, name: string): Step[] {
  const args = JSON.stringify({ url: 'https://api.example-news.com/top?n=5' });
  const text = `Three things: the standup moved to 10:00, the invoice from last month is still unpaid, and it rains from midday.`;
  const steps: Step[] = [
    { after: 80, event: { type: 'chat.turnStart', chatId, frame: 0 } },
    { after: 60, event: { type: 'chat.thinking', chatId, frame: 0, text: `Running ${name}: read, summarise, file.` } },
    { after: 140, event: { type: 'chat.tool', chatId, phase: 'start', frame: 0, id: 1, tool: 'http_read', args } },
    { after: 420, event: { type: 'chat.tool', chatId, phase: 'end', frame: 0, id: 1, tool: 'http_read', args, result: '{"items":5}', durationMs: 417 } },
  ];
  for (const part of text.match(/\s*\S+/g) ?? []) steps.push({ after: 22, event: { type: 'chat.token', chatId, frame: 0, text: part } });
  steps.push({ after: 120, event: { type: 'chat.turnEnd', chatId, frame: 0, tokens: 642 } });
  return steps;
}

/** The list row's subtitle: the first non-empty line, trimmed to something that fits. */
function firstLine(text: string): string {
  const line = text.split('\n').find((l) => l.trim().length) ?? '';
  return line.trim().slice(0, 90);
}
