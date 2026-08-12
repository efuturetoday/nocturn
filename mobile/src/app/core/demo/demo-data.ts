/**
 * The demo's world: the workspace, the chats and their transcripts, the agents, the reminders and
 * the MCP accounts. Data only — `demo-daemon.ts` owns the behaviour and mutates its own copy.
 *
 * Everything time-shaped is derived from a `now` handed in, never from a literal, so the lists read
 * as "12 minutes ago" and "in 2 hours" whenever the app is opened rather than as a frozen date from
 * whenever this file was written.
 *
 * The transcripts are `Message[]` + `ToolNode[][]` — the persisted shapes — so they go through the
 * app's own `buildSnapshotMessages`/`buildForestTools` and render with the same nesting a real
 * reloaded chat would have.
 */

import type {
  Account,
  AgentInfo,
  ChatMeta,
  LibrarySkill,
  LibraryServer,
  MCPInfo,
  Message,
  ReminderInfo,
  SkillInfo,
  ToolNode,
  WorkspaceInfo,
} from '../protocol/nocturn-protocol';

/** One canned chat: its list metadata plus the material a `chat.snapshot` is built from. */
export interface DemoChat {
  meta: ChatMeta;
  messages: Message[];
  /** One nested tool forest per turn (turns are 1:1 with the transcript's user messages). */
  tools: ToolNode[][];
}

export const DEMO_WORKSPACE: WorkspaceInfo = { name: 'main', title: 'main', default: true };

/** The workspaces the demo starts with. A fresh array per call — `demo-daemon.ts` mutates its own. */
export function demoWorkspaces(): WorkspaceInfo[] {
  return [{ ...DEMO_WORKSPACE }];
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

const iso = (t: number): string => new Date(t).toISOString();

/** The chats the demo starts with: two user chats read, one unread, plus one past agent run. */
export function demoChats(now: number): DemoChat[] {
  return [releaseNotes(now), flights(now), briefingRun(now)];
}

export function demoAgents(): AgentInfo[] {
  return [
    {
      name: 'morning-briefing',
      description: 'Reads the calendar and the headlines, then writes the day into a memory note.',
      when: '0 7 * * *',
      autonomy: 'guarded',
      tools: ['http_read', 'memory_read', 'memory_write', 'notify'],
      effort: 'low',
      budgetMs: 120_000,
    },
    {
      name: 'inbox-triage',
      description: 'Sorts the unread mail and flags what actually needs an answer. Manual only.',
      autonomy: 'strict',
      tools: ['http_read', 'notify'],
    },
  ];
}

export function demoReminders(now: number): ReminderInfo[] {
  return [
    { id: 'r-dentist', fireAt: iso(now + 2 * HOUR + 20 * MINUTE), message: 'Call the dentist back about the appointment.' },
    { id: 'r-domain', fireAt: iso(now + 3 * DAY), title: 'Domain', message: 'The nocturn.app domain renews — check the card on file.' },
  ];
}

export function demoAccounts(): Account[] {
  return [
    { server: 'github', connected: true },
    { server: 'linear', connected: false },
  ];
}

/** One skill on and one off, because "off is not gone" is the thing the page has to teach and an
    all-enabled list could not show it. `release-notes` also demonstrates the folder/name split. */
export function demoSkills(): SkillInfo[] {
  return [
    {
      name: 'release-notes',
      folder: 'notes',
      description: 'Turns a diff into notes a human would read.',
      enabled: true,
      bytes: 1_840,
    },
    {
      name: 'standup',
      folder: 'standup',
      description: 'Writes the daily standup from yesterday’s chats.',
      enabled: false,
      bytes: 620,
    },
  ];
}

export function demoSkillBody(name: string): string {
  if (name === 'standup') {
    return [
      '---',
      'name: standup',
      'description: Writes the daily standup from yesterday’s chats.',
      '---',
      '',
      '# Standup',
      '',
      'Read yesterday’s conversations, then answer three things: what moved, what is next,',
      'and what is blocked. One line each. Say "nothing blocked" rather than omitting the line.',
    ].join('\n');
  }
  return [
    '---',
    'name: release-notes',
    'description: Turns a diff into notes a human would read.',
    '---',
    '',
    '# Release notes',
    '',
    'Group the changes by what a user would notice, not by which file moved. Lead each entry',
    'with the effect, then the reason. Drop refactors nobody can see from the outside.',
    '',
    '## Tone',
    '',
    'Past tense, no adjectives, no "we are excited to". A fix says what was broken.',
  ].join('\n');
}

/** One connected server and one waiting on a sign-in — `needs auth` is a state the page renders
    differently from a failure, so the demo has to contain one. */
export function demoServers(): MCPInfo[] {
  return [
    { name: 'github', url: 'https://api.githubcopilot.com/mcp/', state: 'connected', tools: 7 },
    {
      name: 'linear',
      url: 'https://mcp.linear.app/sse',
      state: 'needs auth',
      tools: 0,
      note: 'run: nocturn auth linear',
    },
  ];
}

/** A catalog with two of each. The skill bodies are whole, as the real catalog serves them: the
    Library shows the entire body before installing, so a truncated one would misrepresent the page. */
export function demoCatalog(): { version: string; skills: LibrarySkill[]; mcp: LibraryServer[] } {
  return {
    version: 'demo',
    skills: [
      {
        id: 'commit-messages',
        title: 'Commit messages',
        description: 'Writes commit messages that say why, not what.',
        tags: ['git', 'writing'],
        body: [
          '---',
          'name: commit-messages',
          'description: Writes commit messages that say why, not what.',
          '---',
          '',
          '# Commit messages',
          '',
          'The subject line says what changed for someone reading `git log`, in the imperative.',
          'The body says why it was worth changing. The diff already says what moved.',
        ].join('\n'),
      },
      {
        id: 'travel',
        title: 'Travel planning',
        description: 'Plans a trip and keeps the constraints straight.',
        tags: ['life'],
        body: [
          '---',
          'name: travel',
          'description: Plans a trip and keeps the constraints straight.',
          '---',
          '',
          '# Travel',
          '',
          'Ask for the fixed points first — dates that cannot move, people who must be there.',
          'Everything else is negotiable and should be offered as options, with the cost of each.',
        ].join('\n'),
      },
    ],
    mcp: [
      {
        id: 'linear',
        title: 'Linear',
        description: 'Issues, projects and cycles.',
        homepage: 'https://linear.app',
        tags: ['work'],
        name: 'linear',
        url: 'https://mcp.linear.app/sse',
        auth: 'oauth',
        scopes: ['read', 'write:issue'],
      },
      {
        id: 'weather',
        title: 'Weather',
        description: 'Forecasts, no account needed.',
        tags: ['life'],
        name: 'weather',
        url: 'https://weather.example/mcp',
      },
    ],
  };
}

// ── the transcripts ──────────────────────────────────────────────────────────

/** A finished chat whose turn read a URL and wrote a memory note — two tools, one nested. */
function releaseNotes(now: number): DemoChat {
  const updated = now - 12 * MINUTE;
  const messages: Message[] = [
    { role: 'user', content: 'Draft the release notes for 0.4 from the commits since 0.3.' },
    {
      role: 'assistant',
      content: '',
      toolCalls: [{ id: 't1', tool: 'http_read', args: '{"url":"https://api.github.com/repos/nocturn/nocturn/compare/v0.3...v0.4"}' }],
    },
    { role: 'tool', content: '{"total_commits":37,"files_changed":112}', toolCallID: 't1', durationMs: 412 },
    {
      role: 'assistant',
      content: [
        '37 commits since 0.3. The shape of it:',
        '',
        '**Approvals** now carry the gate action as structure rather than prose, so the phone words the',
        'question itself and a widened grant can never hide beside an exact one.',
        '',
        '**The sandbox** got a wall-clock deadline and a memory cap, both enforced by the host.',
        '',
        '**Knowledge search** landed behind an embedder port, with the index refusing to mix models.',
        '',
        'I saved the full draft as a memory note — say the word and I will push it to the repo.',
      ].join('\n'),
    },
  ];
  const tools: ToolNode[][] = [
    [
      {
        id: 1,
        parent: 0,
        tool: 'http_read',
        args: '{"url":"https://api.github.com/repos/nocturn/nocturn/compare/v0.3...v0.4"}',
        result: '{"total_commits":37,"files_changed":112}',
        durationMs: 412,
      },
      {
        id: 2,
        parent: 0,
        tool: 'memory_write',
        args: '{"path":"drafts/release-0.4.md"}',
        result: 'written',
        durationMs: 18,
      },
    ],
  ];
  return {
    meta: {
      id: 'c1a2b3c4d5e6',
      name: 'Release notes for 0.4',
      source: 'user',
      created: iso(now - 40 * MINUTE),
      updated: iso(updated),
      read: iso(updated), // read
      turns: 1,
      preview: '37 commits since 0.3. The shape of it:',
    },
    messages,
    tools,
  };
}

/** The UNREAD chat — `updated` past `read`, so the list dot and the tab badge are visible. */
function flights(now: number): DemoChat {
  const updated = now - 3 * HOUR;
  const messages: Message[] = [
    { role: 'user', content: 'What is the cheapest week to fly to Lisbon in October?' },
    {
      role: 'assistant',
      content: '',
      toolCalls: [{ id: 't1', tool: 'http_read', args: '{"url":"https://api.example-flights.com/fares?to=LIS&month=10"}' }],
    },
    { role: 'tool', content: '{"cheapest_week":"2026-10-13","median_eur":146}', toolCallID: 't1', durationMs: 733 },
    {
      role: 'assistant',
      content:
        'The week of **13 October** — fares sit around €146 return, about a third under the month median. ' +
        'The two weeks either side are school holidays somewhere in the catchment and cost roughly double.',
    },
  ];
  const tools: ToolNode[][] = [
    [
      {
        id: 1,
        parent: 0,
        tool: 'http_read',
        args: '{"url":"https://api.example-flights.com/fares?to=LIS&month=10"}',
        result: '{"cheapest_week":"2026-10-13","median_eur":146}',
        durationMs: 733,
      },
    ],
  ];
  return {
    meta: {
      id: 'f7e8d9c0b1a2',
      name: 'Cheapest week to fly to Lisbon',
      source: 'user',
      created: iso(now - 3 * HOUR - 4 * MINUTE),
      updated: iso(updated),
      // no `read` — never read, so it shows unread
      turns: 1,
      preview: 'The week of 13 October — fares sit around €146 return,',
    },
    messages,
    tools,
  };
}

/** A past run of the scheduled agent, in the AGENT store — it badges the Agents tab, not Chat. */
function briefingRun(now: number): DemoChat {
  const updated = now - 6 * HOUR;
  const messages: Message[] = [
    { role: 'user', content: 'Run your scheduled task now.' },
    {
      role: 'assistant',
      content: '',
      toolCalls: [{ id: 't1', tool: 'http_read', args: '{"url":"https://api.example-news.com/top?n=5"}' }],
    },
    { role: 'tool', content: '{"items":5}', toolCallID: 't1', durationMs: 289 },
    {
      role: 'assistant',
      content:
        'Three things today: the standup moved to 10:00, the invoice from last month is still unpaid, ' +
        'and it rains from midday. Written to `briefings/today.md`.',
    },
  ];
  const tools: ToolNode[][] = [
    [
      { id: 1, parent: 0, tool: 'http_read', args: '{"url":"https://api.example-news.com/top?n=5"}', result: '{"items":5}', durationMs: 289 },
      { id: 2, parent: 0, tool: 'memory_write', args: '{"path":"briefings/today.md"}', result: 'written', durationMs: 21 },
      { id: 3, parent: 0, tool: 'notify', args: '{"message":"Your briefing is ready."}', result: 'sent', durationMs: 7 },
    ],
  ];
  return {
    meta: {
      id: 'a9b8c7d6e5f4',
      name: 'morning-briefing',
      source: 'agent',
      agent: 'morning-briefing',
      created: iso(now - 6 * HOUR - MINUTE),
      updated: iso(updated),
      read: iso(updated),
      turns: 1,
      preview: 'Three things today: the standup moved to 10:00,',
    },
    messages,
    tools,
  };
}
