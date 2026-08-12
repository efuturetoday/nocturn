import { describe, it, expect } from 'vitest';
import { DENY_OPTION, type ClientCommand, type ServerEvent } from '../protocol/nocturn-protocol';
import { EMPTY, applyEvent, pushUser, seed, type ChatView } from '../services/chat-model';
import { DemoDaemon, type DemoHost } from './demo-daemon';
import { isDemoUrl } from './is-demo';

const WS = 'main';

/**
 * A clock-free host: every scheduled callback goes into a queue drained in virtual-time order, so a
 * turn that takes two seconds of wall clock in the app resolves instantly and deterministically
 * here. Nested schedules (a resume scheduled from inside a drained callback) are picked up by the
 * loop, which is what lets one `drain()` play a whole turn.
 */
class TestHost implements DemoHost {
  readonly events: ServerEvent[] = [];
  private clock = 0;
  private order = 0;
  private queue: { at: number; seq: number; fn: () => void }[] = [];

  now(): number {
    return Date.UTC(2026, 7, 7, 9, 0, 0) + this.clock;
  }

  emit(event: ServerEvent): void {
    this.events.push(event);
  }

  schedule(delayMs: number, fn: () => void): void {
    this.queue.push({ at: this.clock + delayMs, seq: this.order++, fn });
  }

  drain(): void {
    while (this.queue.length) {
      this.queue.sort((a, b) => a.at - b.at || a.seq - b.seq);
      const next = this.queue.shift()!;
      this.clock = next.at;
      next.fn();
    }
  }
}

/** Run commands and return everything they emitted. */
function play(...cmds: ClientCommand[]): { host: TestHost; daemon: DemoDaemon } {
  const host = new TestHost();
  const daemon = new DemoDaemon(host);
  for (const cmd of cmds) daemon.handle(cmd);
  host.drain();
  return { host, daemon };
}

/** Fold a chat's events exactly as ConversationService does: route by chatId, then applyEvent. */
function view(events: ServerEvent[], chatId: string, from: ChatView = EMPTY): ChatView {
  return events
    .filter((e) => 'chatId' in e && e.chatId === chatId)
    .reduce((v, e) => applyEvent(v, e, 1000), from);
}

/** The state a submit leaves behind before any event arrives: the echoed bubble, and running set by
    the service itself (`ConversationService.submit`) rather than by the stream. */
function afterSubmit(text: string): ChatView {
  return { ...pushUser(EMPTY, text), running: true };
}

function typesOf(events: ServerEvent[]): string[] {
  return events.map((e) => e.type);
}

describe('isDemoUrl', () => {
  it('selects on the host alone, and never throws on junk', () => {
    expect(isDemoUrl('ws://demo:8765/ws')).toBe(true);
    expect(isDemoUrl('ws://demo:1234/ws')).toBe(true);
    expect(isDemoUrl('ws://192.168.1.20:8765/ws')).toBe(false);
    expect(isDemoUrl('ws://demo.local:8765/ws')).toBe(false); // a real LAN host, not the demo
    expect(isDemoUrl('not a url')).toBe(false);
    expect(isDemoUrl(null)).toBe(false);
  });
});

describe('the state-sync commands', () => {
  it('answers every list command a connected client sends', () => {
    const { host } = play(
      { cmd: 'workspace.list' },
      { cmd: 'chat.list', ws: WS, kind: 'user' },
      { cmd: 'chat.list', ws: WS, kind: 'agent' },
      { cmd: 'agent.list', ws: WS },
      { cmd: 'reminder.list', ws: WS },
      { cmd: 'auth.list', ws: WS },
      { cmd: 'join.list' },
    );

    expect(typesOf(host.events)).toEqual([
      'workspace.list',
      'chat.list',
      'chat.list',
      'agent.list',
      'reminder.list',
      'auth.accounts',
      'join.list',
    ]);
  });

  it('routes each chat.list to the store that was asked for', () => {
    const { host } = play({ cmd: 'chat.list', ws: WS, kind: 'user' }, { cmd: 'chat.list', ws: WS, kind: 'agent' });
    const [users, agents] = host.events.filter((e) => e.type === 'chat.list');

    expect(users.kind).toBe('user');
    expect(users.chats.every((c) => c.source === 'user')).toBe(true);
    expect(agents.kind).toBe('agent');
    expect(agents.chats.every((c) => c.source === 'agent')).toBe(true);
  });

  it('starts with an unread user chat, so the list dot and the tab badge have something to show', () => {
    const { host } = play({ cmd: 'chat.list', ws: WS, kind: 'user' });
    const [list] = host.events.filter((e) => e.type === 'chat.list');

    expect(list.chats.some((c) => !c.read || c.updated > c.read)).toBe(true);
  });

  it('answers join.list with an EMPTY list — a pending join would auto-present a modal', () => {
    const { host } = play({ cmd: 'join.list' });
    const [joins] = host.events.filter((e) => e.type === 'join.list');

    expect(joins.joins).toEqual([]);
  });

  it('refuses an account connect rather than opening an external browser', () => {
    const { host } = play({ cmd: 'auth.begin', ws: WS, server: 'linear' });
    const [done] = host.events.filter((e) => e.type === 'auth.done');

    expect(done.ok).toBe(false);
    expect(done.error).toBeTruthy();
  });

  it('says nothing for the commands that have no answer', () => {
    const { host } = play({ cmd: 'presence.set', active: true }, { cmd: 'auth.callback', ws: WS, id: 'x', code: 'c', state: 's' });

    expect(host.events).toEqual([]);
  });
});

/** The last event of a type, which after a mutation is the state everyone converged on. */
function lastWorkspaces(events: ServerEvent[]) {
  return events.filter((e) => e.type === 'workspace.list').at(-1)!.items;
}

describe('managing workspaces', () => {
  it('answers a create with the whole new set, the way the daemon broadcasts it', () => {
    const { host } = play({ cmd: 'workspace.create', name: 'work', title: 'Arbeit' });

    expect(lastWorkspaces(host.events)).toEqual([
      { name: 'main', title: 'main', default: true },
      { name: 'work', title: 'Arbeit' },
    ]);
  });

  it('falls a title-less workspace back to its name', () => {
    const { host } = play({ cmd: 'workspace.create', name: 'work' });

    expect(lastWorkspaces(host.events)).toContainEqual({ name: 'work', title: 'work' });
  });

  it('refuses a name that is already taken', () => {
    const { host } = play({ cmd: 'workspace.create', name: 'main', title: 'Second main' });

    expect(typesOf(host.events)).toEqual(['error']);
  });

  it('moves the title and leaves the name alone', () => {
    const { host } = play(
      { cmd: 'workspace.create', name: 'work', title: 'Arbeit' },
      { cmd: 'workspace.rename', name: 'work', title: 'Büro' },
    );

    expect(lastWorkspaces(host.events)).toContainEqual({ name: 'work', title: 'Büro' });
  });

  it('resets the label to the folder name when the title is cleared', () => {
    const { host } = play(
      { cmd: 'workspace.create', name: 'work', title: 'Arbeit' },
      { cmd: 'workspace.rename', name: 'work', title: '' },
    );

    expect(lastWorkspaces(host.events)).toContainEqual({ name: 'work', title: 'work' });
  });

  it('deletes a workspace out of the set', () => {
    const { host } = play(
      { cmd: 'workspace.create', name: 'work', title: 'Arbeit' },
      { cmd: 'workspace.delete', name: 'work' },
    );

    expect(lastWorkspaces(host.events).map((w) => w.name)).toEqual(['main']);
  });

  it('refuses to delete the default, which the daemon would recreate anyway', () => {
    const { host } = play({ cmd: 'workspace.delete', name: 'main' }, { cmd: 'workspace.list' });

    expect(typesOf(host.events)).toEqual(['error', 'workspace.list']);
    expect(lastWorkspaces(host.events).map((w) => w.name)).toEqual(['main']);
  });
});

function lastSkills(events: ServerEvent[]) {
  return events.filter((e) => e.type === 'skill.list').at(-1)!.items;
}

function lastServers(events: ServerEvent[]) {
  return events.filter((e) => e.type === 'mcp.list').at(-1)!.items;
}

describe('managing skills', () => {
  it('starts with one skill off, so "off is not gone" is visible at all', () => {
    const { host } = play({ cmd: 'skill.list', ws: WS });

    expect(lastSkills(host.events).some((s) => !s.enabled)).toBe(true);
  });

  it('keeps a skill in the list when it is switched off', () => {
    const { host } = play({ cmd: 'skill.enable', ws: WS, name: 'release-notes', on: false });
    const items = lastSkills(host.events);

    expect(items.map((s) => s.name)).toContain('release-notes');
    expect(items.find((s) => s.name === 'release-notes')!.enabled).toBe(false);
  });

  it('drops a skill on remove, which is the other command for a reason', () => {
    const { host } = play({ cmd: 'skill.remove', ws: WS, name: 'release-notes' });

    expect(lastSkills(host.events).map((s) => s.name)).not.toContain('release-notes');
  });

  it('answers a read with the body, frontmatter included', () => {
    const { host } = play({ cmd: 'skill.read', ws: WS, name: 'standup' });
    const [body] = host.events.filter((e) => e.type === 'skill.body');

    expect(body.name).toBe('standup');
    expect(body.body.startsWith('---\nname: standup')).toBe(true);
  });
});

describe('managing MCP servers', () => {
  it('reports a new server as connecting BEFORE it reports what happened', () => {
    const { host } = play({ cmd: 'mcp.add', ws: WS, name: 'weather', url: 'https://weather.example/mcp' });
    const lists = host.events.filter((e) => e.type === 'mcp.list');

    expect(lists.length).toBe(2);
    expect(lists[0].items.find((s) => s.name === 'weather')!.state).toBe('connecting');
    expect(lists[1].items.find((s) => s.name === 'weather')!.state).toBe('connected');
  });

  it('lands an oauth server on needs auth rather than on failed', () => {
    const { host } = play({ cmd: 'mcp.add', ws: WS, name: 'linear2', url: 'https://x.example/mcp', auth: 'oauth' });
    const added = lastServers(host.events).find((s) => s.name === 'linear2')!;

    expect(added.state).toBe('needs auth');
    expect(added.note).toBeTruthy();
  });

  it('refuses a name that is already declared', () => {
    const { host } = play({ cmd: 'mcp.add', ws: WS, name: 'github', url: 'https://github.example/mcp' });

    expect(typesOf(host.events)).toEqual(['error']);
  });

  it('drops a server on remove', () => {
    const { host } = play({ cmd: 'mcp.remove', ws: WS, name: 'github' });

    expect(lastServers(host.events).map((s) => s.name)).not.toContain('github');
  });
});

describe('the library', () => {
  it('serves both kinds, and a skill arrives with its whole body', () => {
    const { host } = play({ cmd: 'library.list' });
    const [cat] = host.events.filter((e) => e.type === 'library.catalog');

    expect(cat.skills.length).toBeGreaterThan(0);
    expect(cat.mcp.length).toBeGreaterThan(0);
    expect(cat.skills.every((s) => s.body.includes('---'))).toBe(true);
  });

  it('installs a skill into the workspace list', () => {
    const { host } = play({ cmd: 'library.install', ws: WS, kind: 'skill', id: 'commit-messages' });

    expect(lastSkills(host.events).map((s) => s.name)).toContain('commit-messages');
  });

  it('refuses a second install rather than doing nothing quietly', () => {
    const { host } = play(
      { cmd: 'library.install', ws: WS, kind: 'skill', id: 'commit-messages' },
      { cmd: 'library.install', ws: WS, kind: 'skill', id: 'commit-messages' },
    );

    expect(typesOf(host.events)).toEqual(['skill.list', 'error']);
  });

  it('installs a server through the same connecting-then-outcome path as mcp.add', () => {
    const { host } = play({ cmd: 'library.install', ws: WS, kind: 'mcp', id: 'weather' });
    const lists = host.events.filter((e) => e.type === 'mcp.list');

    expect(lists[0].items.find((s) => s.name === 'weather')!.state).toBe('connecting');
    expect(lists.at(-1)!.items.find((s) => s.name === 'weather')!.state).toBe('connected');
  });

  it('refuses a server the workspace already declares — the catalog holds linear, so does the demo', () => {
    const { host } = play({ cmd: 'library.install', ws: WS, kind: 'mcp', id: 'linear' });

    expect(typesOf(host.events)).toEqual(['error']);
  });
});

describe('the scripted turn', () => {
  const ID = 'aa11bb22cc33';
  const submit: ClientCommand = { cmd: 'chat.submit', ws: WS, kind: 'user', id: ID, text: 'File an issue about the flaky pairing test.' };

  it('parks at the approval instead of finishing the turn', () => {
    const { host } = play(submit);
    const v = view(host.events, ID, afterSubmit(submit.text));
    const [ask] = host.events.filter((e) => e.type === 'approval.request');

    expect(v.running).toBe(true); // no turnEnd yet — the effect is genuinely waiting
    expect(host.events.some((e) => e.type === 'chat.turnEnd')).toBe(false);
    expect(ask).toMatchObject({ chatId: ID, kind: 'net', target: 'api.github.com' });
    expect(ask.options.map((o) => o.id)).toEqual(['once', 'session', 'always', 'widen0']);
    expect(ask.options.some((o) => o.id === DENY_OPTION)).toBe(false); // deny is never offered
  });

  it('names the parked tool call, so its frame freezes rather than ticking on', () => {
    const { host } = play(submit);
    const [ask] = host.events.filter((e) => e.type === 'approval.request');
    const v = view(host.events, ID, afterSubmit(submit.text));
    const parked = v.messages.at(-1)!.tools.find((t) => t.id === ask.frame);

    expect(parked).toMatchObject({ tool: 'http_write', running: true });
  });

  it('nests the sub-call under the frame that made it', () => {
    const { host } = play(submit);
    const tools = view(host.events, ID, afterSubmit(submit.text)).messages.at(-1)!.tools;

    expect(tools.find((t) => t.tool === 'code_run')).toMatchObject({ depth: 0 });
    expect(tools.find((t) => t.tool === 'http_read')).toMatchObject({ depth: 1 });
  });

  it('finishes the turn on an allow', () => {
    const host = new TestHost();
    const daemon = new DemoDaemon(host);
    daemon.handle(submit);
    host.drain();
    const ask = host.events.find((e) => e.type === 'approval.request')!;

    daemon.handle({ cmd: 'approval.resolve', id: ask.id, option: 'once' });
    host.drain();

    const v = view(host.events, ID, afterSubmit(submit.text));
    expect(host.events.some((e) => e.type === 'approval.resolved' && e.id === ask.id)).toBe(true);
    expect(v.running).toBe(false);
    expect(v.messages.at(-1)!.content).toContain('Filed as');
    expect(v.messages.at(-1)!.tools.every((t) => !t.running)).toBe(true);
    expect(v.messages.at(-1)!.tools.find((t) => t.tool === 'http_write')!.err).toBeUndefined();
  });

  it('finishes the turn on a deny, with the effect refused', () => {
    const host = new TestHost();
    const daemon = new DemoDaemon(host);
    daemon.handle(submit);
    host.drain();
    const ask = host.events.find((e) => e.type === 'approval.request')!;

    daemon.handle({ cmd: 'approval.resolve', id: ask.id, option: DENY_OPTION });
    host.drain();

    const v = view(host.events, ID, afterSubmit(submit.text));
    expect(v.running).toBe(false);
    expect(v.messages.at(-1)!.content).toContain('nothing was written');
    expect(v.messages.at(-1)!.tools.find((t) => t.tool === 'http_write')!.err).toBeTruthy();
  });

  it('treats an option it never offered as a refusal', () => {
    const host = new TestHost();
    const daemon = new DemoDaemon(host);
    daemon.handle(submit);
    host.drain();
    const ask = host.events.find((e) => e.type === 'approval.request')!;

    daemon.handle({ cmd: 'approval.resolve', id: ask.id, option: 'widen9' });
    host.drain();

    const v = view(host.events, ID, afterSubmit(submit.text));
    expect(v.messages.at(-1)!.tools.find((t) => t.tool === 'http_write')!.err).toBeTruthy();
  });

  it('ends the turn and clears the approval on a cancel', () => {
    const host = new TestHost();
    const daemon = new DemoDaemon(host);
    daemon.handle(submit);
    host.drain();
    const ask = host.events.find((e) => e.type === 'approval.request')!;
    const before = host.events.length;

    daemon.handle({ cmd: 'chat.cancel', ws: WS, kind: 'user', id: ID });
    host.drain();

    const after = host.events.slice(before);
    expect(after.some((e) => e.type === 'approval.resolved' && e.id === ask.id)).toBe(true);
    expect(after.some((e) => e.type === 'chat.turnEnd' && e.err === 'cancelled')).toBe(true);
    expect(view(host.events, ID, afterSubmit(submit.text)).running).toBe(false);
  });
});

describe('snapshots', () => {
  it('hands over the persisted transcript with its nested tool forest', () => {
    const { host } = play({ cmd: 'chat.list', ws: WS, kind: 'user' }, { cmd: 'chat.open', ws: WS, kind: 'user', id: 'c1a2b3c4d5e6' });
    const snap = host.events.find((e) => e.type === 'chat.snapshot')!;
    const v = seed(snap, 1000);

    expect(v.running).toBe(false);
    expect(v.messages[0]).toMatchObject({ role: 'user' });
    expect(v.messages[1].tools.map((t) => t.tool)).toEqual(['http_read', 'memory_write']);
  });

  it('hands a mid-turn reopen the SAME material the live stream carried', () => {
    const id = 'dd44ee55ff66';
    const text = 'Check whether the release is tagged.';
    const host = new TestHost();
    const daemon = new DemoDaemon(host);
    daemon.handle({ cmd: 'chat.submit', ws: WS, kind: 'user', id, text });
    host.drain();
    const live = view(host.events, id, afterSubmit(text));

    daemon.handle({ cmd: 'chat.open', ws: WS, kind: 'user', id });
    host.drain();
    const snap = host.events.find((e) => e.type === 'chat.snapshot')!;

    // The running turn is NOT in `messages` — it rides as inflight material and folds by the same
    // reducer, so a reopen mid-turn and the live stream land on identical state.
    expect(snap.messages).toEqual([]);
    expect(snap.inflightRunning).toBe(true);
    expect(seed(snap, 1000)).toEqual(live);
  });

  it('persists a finished turn so reopening the chat still shows it', () => {
    const id = '112233445566';
    const text = 'Summarise the week.';
    const host = new TestHost();
    const daemon = new DemoDaemon(host);
    daemon.handle({ cmd: 'chat.submit', ws: WS, kind: 'user', id, text });
    host.drain();
    const ask = host.events.find((e) => e.type === 'approval.request')!;
    daemon.handle({ cmd: 'approval.resolve', id: ask.id, option: 'session' });
    host.drain();

    daemon.handle({ cmd: 'chat.open', ws: WS, kind: 'user', id });
    host.drain();
    const snap = host.events.filter((e) => e.type === 'chat.snapshot').at(-1)!;
    const v = seed(snap, 1000);

    expect(snap.inflightRunning).toBeUndefined();
    expect(v.messages.map((m) => m.role)).toEqual(['user', 'assistant']);
    expect(v.messages[0].content).toBe(text);
    expect(v.messages[1].content).toContain('Filed as');
    expect(v.messages[1].tools.map((t) => t.tool)).toEqual(['http_read', 'code_run', 'http_write']);
  });

  it('answers a freshly minted id with an empty transcript', () => {
    const { host } = play({ cmd: 'chat.open', ws: WS, kind: 'user', id: '999888777666' });
    const snap = host.events.find((e) => e.type === 'chat.snapshot')!;

    expect(snap.messages).toEqual([]);
    expect(seed(snap, 1000)).toEqual(EMPTY);
  });
});

describe('the rest of the world', () => {
  it('advances the read cursor and tells every device', () => {
    const { host } = play({ cmd: 'chat.markRead', ws: WS, kind: 'user', id: 'f7e8d9c0b1a2' });
    const [activity] = host.events.filter((e) => e.type === 'chat.activity');

    expect(activity.chat.read).toBe(activity.chat.updated);
  });

  it('drops a reminder and lets the client re-list rather than guessing', () => {
    const host = new TestHost();
    const daemon = new DemoDaemon(host);
    daemon.handle({ cmd: 'reminder.cancel', ws: WS, id: 'r-dentist' });
    host.drain();
    expect(host.events.some((e) => e.type === 'reminder.changed')).toBe(true);

    daemon.handle({ cmd: 'reminder.list', ws: WS });
    host.drain();
    const list = host.events.filter((e) => e.type === 'reminder.list').at(-1)!;
    expect(list.reminders.map((r) => r.id)).not.toContain('r-dentist');
  });

  it('fires an agent into the agent store and wakes the device when it finishes', () => {
    const host = new TestHost();
    const daemon = new DemoDaemon(host);
    daemon.handle({ cmd: 'agent.fire', ws: WS, name: 'morning-briefing' });
    host.drain();

    const created = host.events.find((e) => e.type === 'chat.activity')!;
    expect(created.chat.source).toBe('agent');
    expect(created.chat.agent).toBe('morning-briefing');

    const v = view(host.events, created.chat.id);
    expect(v.running).toBe(false);
    expect(v.messages.at(-1)!.content).toContain('standup');

    const note = host.events.find((e) => e.type === 'notification')!;
    expect(note).toMatchObject({ kind: 'notify', chatId: created.chat.id });

    daemon.handle({ cmd: 'chat.list', ws: WS, kind: 'agent' });
    host.drain();
    const list = host.events.filter((e) => e.type === 'chat.list').at(-1)!;
    expect(list.chats.map((c) => c.id)).toContain(created.chat.id);
  });
});
