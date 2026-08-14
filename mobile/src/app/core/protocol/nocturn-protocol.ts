/**
 * Nocturn wire protocol (greenfield) — TypeScript definitions.
 *
 * One WebSocket carries tagged JSON: the client sends `{cmd:"domain.action", …}`, the server sends
 * `{type:"domain.action", …}`. Discriminate on `cmd` (client) / `type` (server). Pairing and auth are
 * HTTP (see the bottom); every `/ws` connection carries a paired device's bearer.
 *
 * Mirrors app/serve (chat.go, join.go, approval.go, presence.go) + app/auth. Nothing here grants
 * authority: gated effects still go through the workspace gate + out-of-band approval.
 */

// ── shared value types ───────────────────────────────────────────────────────

/** Who a chat belongs to — a person or a fired agent (which store holds it). */
export type Source = "user" | "agent";

/** A device platform, selecting the push provider (ios→APNs, android→FCM). */
export type Platform = "ios" | "android" | "web";

/**
 * One workspace (an isolated stack of chats/tools/grants) the daemon serves.
 *
 * The two names are not interchangeable. `name` is the IDENTITY: it is the directory on disk, an
 * input to that workspace's vault key, and the `ws` field on every other command — so it never
 * changes, and a screen that hides it teaches the wrong model of what renaming does. `title` is only
 * what to show; the daemon falls it back to `name`, so it is never empty.
 */
export interface WorkspaceInfo {
  name: string;
  title: string;
  default?: boolean; // the workspace the daemon recreates at startup — it cannot be deleted
}

/** One chat's summary (chat.list). The name is derived from the first message. */
export interface ChatMeta {
  id: string;
  name: string;
  source: Source;
  agent?: string; // the owning agent's name (agent runs only) — for grouping runs under a roster agent
  created: string; // RFC3339
  updated: string; // RFC3339
  read?: string; // RFC3339 shared read cursor; unread when updated > read (absent = never read)
  turns: number;
  preview?: string; // last message's first line — the list row's subtitle (à la Apple Mail)
}

/** How a declared agent's SCHEDULED firing resolves an Ask: strict denies unattended, guarded asks
    the human out of band (the phone). */
export type Autonomy = "strict" | "guarded";

/** One declared agent (agent.list) — its identity, schedule, autonomy and tool cage. NOT a run. */
export interface AgentInfo {
  name: string;
  description?: string;
  when?: string; // cron schedule; absent/empty = manual only
  autonomy: Autonomy;
  tools?: string[]; // the tool cage
  effort?: string; // reasoning effort
  budgetMs?: number; // per-run wall-clock; 0/absent = workspace default
}

/**
 * One skill a workspace holds (skill.list).
 *
 * `name` is the ADDRESS — every skill command takes it — and `folder` is only where it happens to
 * live. Skills are the one place in the tree where the folder is not the identity: the name in
 * SKILL.md's frontmatter wins, so a skill called `deploy` may sit in a folder called anything.
 * Showing both is what stops "rename the folder" from looking like it would work.
 *
 * `enabled: false` does not mean gone. The folder moves under skills/.disabled/, the assistant stops
 * seeing it, and everything shipped alongside it stays — a switch, not a deletion.
 *
 * `bytes` is the size of SKILL.md. It is here because a skill costs context on every turn, which is
 * the one cost of holding one that is otherwise invisible.
 */
export interface SkillInfo {
  /** The plugin that BUNDLED this skill, absent for a skill of its own in skills/. A bundled one can
      be neither switched off nor deleted: it belongs to the plugin and goes when the plugin does. */
  plugin?: string;
  name: string;
  folder: string;
  description?: string;
  enabled: boolean;
  bytes: number;
}

/**
 * What became of one declared MCP server (mcp.list).
 *
 * `connecting` is a real state, not a spinner: the daemon writes the declaration, says so at once,
 * and runs the handshake afterwards — each server is allowed thirty seconds, and the connection
 * could not carry a chat message while it waited. `needs auth` is an errand rather than a failure.
 * `note` says why in the words of the log, for the two states that owe an explanation.
 */
export type MCPState = "connecting" | "connected" | "needs auth" | "failed";

/** One declared MCP server and how it fared. Declared, not connected: a server that did not come up
    is exactly what this list is opened to find, so it stays in it. */
export interface MCPInfo {
  name: string;
  url: string;
  state: MCPState;
  tools: number;
  note?: string;
}

/**
 * One installable skill in the catalog (library.catalog).
 *
 * The whole `body` rides along with the listing rather than being fetched on demand. That is
 * deliberate: the app shows it before installing, and a second round-trip for something the daemon
 * already holds would only make that step skippable — and it is the step worth not skipping.
 */
export interface LibrarySkill {
  id: string;
  title: string;
  description: string;
  homepage?: string;
  tags?: string[];
  body: string;
}

/** One installable MCP server in the catalog. `scopes` is what a sign-in would ask for, shown before
    the browser opens. The client id and secret are the daemon's and deliberately never on the wire. */
export interface LibraryServer {
  id: string;
  title: string;
  description: string;
  homepage?: string;
  tags?: string[];
  name: string;
  url: string;
  auth?: string;
  scopes?: string[];
}

/**
 * One installable plugin in the catalog.
 *
 * A plugin is CODE, so this entry carries more than a listing: `tools`, `uses`, `hosts` and `scopes`
 * are pulled out of its manifest by the daemon so a client can show what installing GRANTS without
 * parsing JSON. That triple is the review surface — the sandbox contains what the code can do, but
 * the manifest is what it asks for. Render it before offering the button.
 *
 * `manifest`, `script` and `skill` ride along whole, the way a skill's body does. The signature is
 * checked by the daemon before an entry is ever offered, so anything listed here verified against a
 * key compiled into it — the client neither sees nor checks one.
 */
export interface LibraryPlugin {
  id: string;
  title: string;
  description: string;
  homepage?: string;
  tags?: string[];
  /** The folder it installs under, and the prefix on every tool it exposes. */
  name: string;
  /** Tool names, already namespaced by the daemon. */
  tools: string[];
  /** The base tools its guest may call — its cage. Empty means it reaches nothing. */
  uses: string[];
  /** Where a declared credential would ride. */
  hosts?: string[];
  /** What a sign-in would ask for. */
  scopes?: string[];
  manifest: string;
  script: string;
  /** Instructions it bundles, which join the prompt catalog on install. */
  skill?: string;
}

/** One model-issued tool call inside a transcript message. */
export interface ToolCall {
  id: string;
  tool: string;
  args: string; // JSON
}

/**
 * One conversation message in a snapshot (the persisted transcript). An assistant message that only
 * issued tool calls has empty `content` and a non-empty `toolCalls`; a tool result is a `tool` message
 * whose `toolCallID` links it to the call it answers.
 */
export interface Message {
  role: "system" | "user" | "assistant" | "tool";
  content?: string;
  toolCalls?: ToolCall[];
  toolCallID?: string;
  durationMs?: number; // on a tool-result message: the call's wall-clock (persisted, survives reload)
}

/** A pending second-device join, shown to an already-paired device (join.list) so a human relays the code. */
export interface PendingJoin {
  joinId: string;
  name: string;
  platform?: Platform;
  code: string;
}

// ── server → client events (discriminate on `type`) ──────────────────────────

// The streaming events all carry `chatId`: the daemon broadcasts every live chat's events to every
// device (no per-connection subscription), so the client applies only those for the chat it shows.

/** One streamed chunk of the assistant's answer. `frame` is the enclosing call (0 = top-level). */
export interface ChatToken {
  type: "chat.token";
  chatId: string;
  frame: number;
  text: string;
}

/** One streamed chunk of the model's reasoning (render dim). */
export interface ChatThinking {
  type: "chat.thinking";
  chatId: string;
  frame: number;
  text: string;
}

/** A tool call's start or end. Group by `frame` to nest sub-agent activity; `id` is the call instance. */
export interface ChatTool {
  type: "chat.tool";
  chatId: string;
  phase: "start" | "end";
  frame: number;
  id: number;
  tool: string;
  args: string;
  result?: string; // end only
  err?: string; // end only (e.g. a denied effect)
  durationMs?: number; // end only — the call's wall-clock (server-measured)
}

/** A turn starting: the client opens a fresh assistant bubble from it, so the answer bubble comes
    deterministically from the stream (a local turn and a backend-initiated one render identically). */
export interface ChatTurnStart {
  type: "chat.turnStart";
  chatId: string;
  frame: number;
}

/** A turn finishing, with its stop reason (if any) and the turn's total tokens. */
export interface ChatTurnEnd {
  type: "chat.turnEnd";
  chatId: string;
  frame: number;
  err?: string;
  tokens: number;
}


/**
 * One captured tool call in a turn's forest (observability, not part of the transcript). `parent` is
 * the enclosing call's id (0 = top level) — it reconstructs the nesting the live stream shows,
 * including nested host-bridge calls (code_run→http_read) and sub-agent internals.
 */
export interface ToolNode {
  id: number;
  parent: number;
  tool: string;
  args?: string;
  result?: string;
  err?: string;
  durationMs?: number;
}

/**
 * A chat's persisted transcript plus its per-turn tool forest, sent on chat.open. `tools[k]` is the
 * k-th turn's forest (turns are 1:1 with the transcript's user messages); the client zips group k onto
 * the k-th assistant bubble. Absent/empty groups fall back to the flat toolCalls in the messages.
 *
 * The RUNNING turn (if any) is NOT yet in `messages` (the transcript persists only at turn end), so it
 * is handed over as raw material the client folds with the SAME reducer as the live stream, rather than
 * a pre-rendered model: `inflightInput` is the user's message (not a stream event) and `inflightEvents`
 * are the turn's events so far — identical to the live broadcast. The client replays them so a reopen
 * mid-turn and a live turn render by one path. `inflightRunning` gates it (a turn can be running with
 * its input recorded before the first event streams).
 */
export interface ChatSnapshot {
  type: "chat.snapshot";
  id: string;
  messages: Message[];
  tools?: ToolNode[][];
  inflightRunning?: boolean;
  inflightInput?: string;
  inflightEvents?: ServerEvent[];
}

/** A workspace's chat list, replying to chat.list. `kind` echoes the requested store so a client that
    lists both routes each result to the right view. */
export interface ChatList {
  type: "chat.list";
  ws: string;
  kind: Source;
  chats: ChatMeta[];
}

/** A workspace's declared-agent roster, replying to agent.list. */
export interface AgentListEvent {
  type: "agent.list";
  ws: string;
  agents: AgentInfo[];
}

/** The daemon's workspaces, replying to workspace.list. */
export interface WorkspaceList {
  type: "workspace.list";
  items: WorkspaceInfo[];
}

/**
 * Pushed to every device when a chat changes (a turn ended, a markRead) so lists update live — its
 * unread dot raises or clears without the device streaming that chat.
 */
export interface ChatActivity {
  type: "chat.activity";
  ws: string;
  chat: ChatMeta;
}

/** The pending second-device joins with their codes, replying to join.list (paired devices only). */
export interface JoinList {
  type: "join.list";
  joins: PendingJoin[];
}

/** One enrolled device. The bearer hash never crosses the wire — the daemon blanks it at the source. */
export interface EnrolledDevice {
  id: string;
  name: string;
  /** app | web | appliance | tool — what it IS, not what it may do. */
  class?: string;
  platform?: Platform;
  added: string;
  lastUsed?: string;
}

/**
 * The household's devices, replying to device.list, and pushed to every device that may enrol
 * whenever one is forgotten — two admin screens must not show two different answers.
 */
export interface DeviceList {
  type: "device.list";
  devices: EnrolledDevice[];
  /** The id of the device this connection belongs to, so "this device" needs no guessing by name. */
  self?: string;
}

/**
 * An out-of-band approval request, as STRUCTURE rather than prose: `kind` and `target` are the gate
 * action verbatim and `options` are the answers the daemon minted for this exact approval. The
 * wording is ours — a `kind` is a closed set we map to our own label, a `target` is data we render as
 * data. Answer with an `approval.resolve` echoing an option's `id`.
 */
export interface ApprovalRequest {
  type: "approval.request";
  id: string;
  frame?: number; // the tool call this approval is for (freeze that tool's timer); absent = not tool-scoped
  chatId?: string; // the chat/agent run whose turn raised this — for provenance; absent = not chat-scoped
  kind: string; // the gate axis: "net" | "file" | "memory" | "notify" | "remind" | …
  target?: string; // the host/path; absent = a kind with no target
  options: ApprovalOption[];
}

/**
 * One answer on offer. `id` is opaque — echo it back, never mint one, so a device can only choose
 * among grants the daemon offered. `recall` is how long the answer would be remembered. `widen` is
 * present ONLY for a suggested widening and carries the broader grant that answer would create: its
 * presence is the whole "is this a widening?" question. Recall is duration, widen is reach — two
 * axes, and a widened always is the broadest answer on the sheet.
 */
export interface ApprovalOption {
  id: string;
  recall: "never" | "session" | "always";
  widen?: ApprovalGrant;
}

/** A {kind,target} grant pattern — the same pair the daemon writes to `grants.json`. */
export interface ApprovalGrant {
  kind: string;
  target: string;
}

/** A pending approval concluded (answered here or elsewhere, timed out, or cancelled) — clear the prompt. */
export interface ApprovalResolved {
  type: "approval.resolved";
  id: string;
}

/** A workspace's skills, disabled ones included — a list that omitted them could not offer
    switching one back on. Replies to skill.list, and broadcast after every change. */
export interface SkillList {
  type: "skill.list";
  ws: string;
  items: SkillInfo[];
}

/**
 * One installed plugin, replying to plugin.list and broadcast after an install.
 *
 * The name it was filed under and how many tools it contributed. What it may REACH is not here on
 * purpose: that is decided by its manifest and asked about at the gate, and the catalog entry — which
 * carries the manifest — is where a person reads it before agreeing.
 */
export interface PluginInfo {
  name: string;
  tools: number;
}

/** A workspace's installed plugins (plugin.list). */
export interface PluginList {
  type: "plugin.list";
  ws: string;
  items: PluginInfo[];
}

/** Request a workspace's installed plugins (→ PluginList). Listing grants nothing. */
export interface PluginListCmd {
  cmd: "plugin.list";
  ws: string;
}

/** One skill's SKILL.md, verbatim and WITH its frontmatter, replying to skill.read. Verbatim because
    the point of reading one is to see exactly what the model is told. */
export interface SkillBody {
  type: "skill.body";
  ws: string;
  name: string;
  body: string;
}

/**
 * A workspace's MCP servers, replying to mcp.list and broadcast after every change.
 *
 * It arrives TWICE for one mutation: immediately, carrying `connecting` for a server nobody has
 * tried yet, and again once the reload's handshakes are through. A client that renders only the
 * second frame shows nothing for up to thirty seconds; one that renders only the first never
 * learns the outcome.
 */
export interface MCPList {
  type: "mcp.list";
  ws: string;
  items: MCPInfo[];
}

/** The installable catalog, replying to library.list / library.refresh. Not workspace-scoped: a
    catalog is the same everywhere the daemon serves; only installing picks a target. */
export interface LibraryCatalog {
  type: "library.catalog";
  version: string;
  skills: LibrarySkill[];
  mcp: LibraryServer[];
  plugins: LibraryPlugin[];
}

/**
 * One pending reminder. `fireAt` is RFC3339 WITH an offset, so it denotes the instant the daemon
 * intended — parse it, don't read the wall clock off the string.
 */
export interface ReminderInfo {
  id: string;
  fireAt: string;
  message: string;
  title?: string;
}

/**
 * A workspace's pending reminders, soonest first, replying to reminder.list. A fired reminder is
 * gone from this set (it arrives as a push instead) — this is never a history.
 */
export interface ReminderList {
  type: "reminder.list";
  ws: string;
  reminders: ReminderInfo[];
}

/**
 * Broadcast to every device when a workspace's pending reminders change (the model set one, one was
 * cancelled, one fired). It carries no payload on purpose: re-list, so devices converge on the
 * daemon's set rather than on their own optimistic guesses.
 */
export interface ReminderChanged {
  type: "reminder.changed";
  ws: string;
}

/**
 * A proactive message delivered to an AWAKE device over the live connection — a reminder that just
 * fired, or a `notify` tool call. It is the in-app half of a delivery whose other half is an APNs
 * push: the push is suppressed or easy to miss while the app is in the foreground, and a fired
 * reminder leaves the pending list immediately, so without this the phone-in-hand case would show
 * nothing. Expect to receive BOTH on occasion — show this one and let the OS drop its duplicate.
 *
 * `chatId`, when set, is the chat or agent run it came from: what a tap should open.
 */
export interface Notification {
  type: "notification";
  ws: string;
  kind: "remind" | "notify";
  chatId?: string;
  title?: string;
  message: string;
}

/** One connectable MCP account (auth.accounts): a discover-mode server and whether it holds a token. */
export interface Account {
  server: string;
  connected: boolean;
}

/** A workspace's connectable MCP accounts and their status (reply to auth.list). */
export interface AuthAccounts {
  type: "auth.accounts";
  ws: string;
  accounts: Account[];
}

/**
 * A consent URL to open in an in-app browser (reply to auth.begin). Open `url`, watch the browser for
 * a navigation whose URL starts with `redirectPrefix`, lift `code`+`state` from its query, then send
 * them back as auth.callback with this same `id`. The token is minted in the daemon — only the
 * single-use, PKCE-bound code ever travels back.
 */
export interface AuthOpen {
  type: "auth.open";
  id: string;
  server: string;
  url: string;
  redirectPrefix: string;
}

/** The outcome of a connect attempt: connected, or an error to show. Correlated by `id` once a
    session exists; a failure during auth.begin carries only `server` (no id minted yet). */
export interface AuthDone {
  type: "auth.done";
  id?: string;
  server?: string;
  ok: boolean;
  error?: string;
}

/** A control error (e.g. an unknown command). */
export interface ErrorEvent {
  type: "error";
  text: string;
}

/** The closed union of everything the server sends. Switch on `type`. */
export type ServerEvent =
  | ChatTurnStart
  | ChatToken
  | ChatThinking
  | ChatTool
  | ChatTurnEnd
  | ChatSnapshot
  | ChatList
  | AgentListEvent
  | WorkspaceList
  | SkillList
  | SkillBody
  | PluginList
  | MCPList
  | LibraryCatalog
  | ChatActivity
  | JoinList
  | DeviceList
  | ApprovalRequest
  | ApprovalResolved
  | ReminderList
  | ReminderChanged
  | Notification
  | AuthAccounts
  | AuthOpen
  | AuthDone
  | ErrorEvent;

// ── client → server commands (discriminate on `cmd`) ─────────────────────────

// `kind` ("user" | "agent") is MANDATORY on every store-addressed chat command: the daemon validates
// it and routes to the right store. The client never tracks it as mutable state — it is a structural
// constant of which conversation you are in (see the ConversationService subclasses).

/** Send a message to chat `id` (client-minted). An unknown id starts that chat, a known one appends. */
export interface ChatSubmit {
  cmd: "chat.submit";
  ws: string;
  kind: Source;
  text: string;
  id: string;
}

/** Resume a chat in workspace `ws`: the server replies with a ChatSnapshot, then streams its turns. */
export interface ChatOpen {
  cmd: "chat.open";
  ws: string;
  kind: Source;
  id: string;
}

/** Request a workspace's chat list (→ ChatList). `kind` selects the store: user chats or agent runs. */
export interface ChatListCmd {
  cmd: "chat.list";
  ws: string;
  kind: Source;
}

/** Abort a chat's running turn (id-addressed; the chat and session stay open). */
export interface ChatCancel {
  cmd: "chat.cancel";
  ws: string;
  kind: Source;
  id: string;
}

/** Mark a chat read up to its latest turn — the daemon advances a shared cursor for every device. */
export interface ChatMarkRead {
  cmd: "chat.markRead";
  ws: string;
  kind: Source;
  id: string;
}

/** Request a workspace's declared-agent roster (→ AgentListEvent). */
export interface AgentListCmd {
  cmd: "agent.list";
  ws: string;
}

/** Trigger an agent run now. `task` defaults to the scheduled prompt when omitted. Fire-and-forget:
    no direct reply — the run appears via chat.activity and streams over the agent-kind chat events. */
export interface AgentFireCmd {
  cmd: "agent.fire";
  ws: string;
  name: string;
  task?: string;
}

/** Request the daemon's workspaces (→ WorkspaceList). */
export interface WorkspaceListCmd {
  cmd: "workspace.list";
}

/**
 * Add a workspace. None of the three mutations carries `ws`: the set is daemon-wide, not something
 * one workspace holds. All three take the `manage` capability, which an appliance does not have, and
 * none is answered directly — the daemon re-broadcasts the whole workspace.list to every connection,
 * so a device that did not ask still converges. A rejection arrives as a bare `error`, uncorrelated.
 *
 * `name` must match ^[a-z0-9][a-z0-9_-]*$ — it becomes a directory and a key-derivation component,
 * not a label. `title` is optional; without one the list shows the name.
 */
export interface WorkspaceCreateCmd {
  cmd: "workspace.create";
  name: string;
  title?: string;
}

/**
 * Change a workspace's DISPLAY title. The identity is untouched, and deliberately: the folder name
 * is what its vault and every plugin/MCP shard are keyed from, so moving it would re-key them into
 * unreadability. An empty title clears the override and the folder name shows again.
 */
export interface WorkspaceRenameCmd {
  cmd: "workspace.rename";
  name: string;
  title: string;
}

/**
 * Remove a workspace. The daemon closes it and MOVES its directory to a trash folder rather than
 * deleting it — what is in there is every conversation, every note and a vault, and the person doing
 * this is on a phone, on a list, possibly by accident. The default workspace refuses: it is
 * recreated at startup, so removing it would appear to work and then undo itself.
 */
export interface WorkspaceDeleteCmd {
  cmd: "workspace.delete";
  name: string;
}

/**
 * Re-run a workspace's discovery: its agents, skills, plugins and MCP servers, read from disk again.
 *
 * The answer for everything that changed WITHOUT a command — a skill folder copied in, an agent
 * edited, a server that was down when the daemon last looked, an account just authorized. There is no
 * watcher on those directories, deliberately: a ticker would re-run every MCP handshake against other
 * people's servers on a schedule, and a filesystem watcher means a dependency, recursive watch
 * management and events dropped under load.
 *
 * It is a WORKSPACE command rather than an MCP one because re-running discovery re-runs all of it;
 * a command named for one part would promise a part and do the whole. Both `skill.list` and
 * `mcp.list` follow when it lands, which is right for the command you reach for when you do not know
 * which of them changed.
 */
export interface WorkspaceReloadCmd {
  cmd: "workspace.reload";
  ws: string;
}

/** Request a workspace's skills (→ SkillList). Ungated: a skill is CONTEXT, never authority — it
    shapes how the model uses its gated tools and grants nothing itself — so any device may look. */
export interface SkillListCmd {
  cmd: "skill.list";
  ws: string;
}

/** Read one skill's SKILL.md (→ SkillBody). Ungated for the same reason as the listing. */
export interface SkillReadCmd {
  cmd: "skill.read";
  ws: string;
  name: string;
}

/**
 * Switch a skill on or off. Off moves its folder under skills/.disabled/ — the assistant stops
 * seeing it and nothing is lost, which is why this is a switch and `skill.remove` is a separate,
 * heavier command. Takes `manage`; the daemon broadcasts the new list.
 *
 * The change lands on the NEXT turn, never inside a running one: the model is handed its tool list
 * when a turn starts and plans against it, so a tool may not vanish between two calls of the same
 * turn.
 */
export interface SkillEnableCmd {
  cmd: "skill.enable";
  ws: string;
  name: string;
  on: boolean;
}

/** Delete a skill's directory. This one really deletes — there is no trash, unlike a workspace —
    but anything from the catalog can be installed again. Takes `manage`. */
export interface SkillRemoveCmd {
  cmd: "skill.remove";
  ws: string;
  name: string;
}

/** Request a workspace's MCP servers (→ MCPList). Ungated: which servers are declared, and which of
    them failed, is the same kind of fact as which workspaces exist. */
export interface MCPListCmd {
  cmd: "mcp.list";
  ws: string;
}

/**
 * Declare an MCP server. NO credential rides along, by design: a bearer token is seeded on the host
 * with `nocturn secret set`, and OAuth runs through the auth.* domain this app already speaks. The
 * form therefore asks for a name, a URL and an auth mode and nothing else.
 *
 * `name` must match ^[a-z0-9][a-z0-9_-]{0,31}$ and the URL must be https — it becomes a folder, a
 * secret shard key and a tool-name prefix. Answered with mcp.list twice (see MCPList).
 */
export interface MCPAddCmd {
  cmd: "mcp.add";
  ws: string;
  name: string;
  url: string;
  auth?: string;
}

/**
 * Drop a server: its declaration, its secret shard, and the remembered network grant for its host.
 * That last part is worth saying out loud before it happens — the grant may have been given for
 * `http_read` on the same host, and dropping it means being asked once more.
 */
export interface MCPRemoveCmd {
  cmd: "mcp.remove";
  ws: string;
  name: string;
}

/** Request the installable catalog (→ LibraryCatalog). Browsing grants nothing, so it is ungated.
    Fetching is lazy and cached on the daemon; this is the cheap call. */
export interface LibraryListCmd {
  cmd: "library.list";
}

/** Re-fetch the catalog past the daemon's cache, then answer like library.list. Pull-to-refresh. */
export interface LibraryRefreshCmd {
  cmd: "library.refresh";
}

/**
 * Install one catalog entry into a workspace. It carries an ID and nothing else, and that is the
 * whole security shape of this domain: a command carrying a skill BODY would be a way to put
 * arbitrary text into every system prompt of every turn. The content is looked up server-side. Do
 * not add a body or a URL field here — the daemon has an invariant test that says so.
 *
 * Answered with the target domain's list (skill.list, or mcp.list twice). Installing something
 * already held is REFUSED with a message rather than silently ignored.
 */
export interface LibraryInstallCmd {
  cmd: "library.install";
  ws: string;
  kind: "skill" | "mcp" | "plugin";
  id: string;
}

/** Request a workspace's pending reminders (→ ReminderList). */
export interface ReminderListCmd {
  cmd: "reminder.list";
  ws: string;
}

/**
 * Drop a pending reminder. There is deliberately no create command: reminders are set by the model
 * through the gated `remind` tool, so a device may view and cancel but never mint one. The daemon
 * answers with a reminder.changed broadcast, not a direct reply.
 */
export interface ReminderCancelCmd {
  cmd: "reminder.cancel";
  ws: string;
  id: string;
}

/** Request the pending second-device joins with codes (→ JoinList). */
export interface JoinListCmd {
  cmd: "join.list";
}

/** Request the household's enrolled devices (→ DeviceList). Devices that may enrol only. */
export interface DeviceListCmd {
  cmd: "device.list";
}

/**
 * Revoke one device's bearer. The exit from "my phone is lost": until this existed the only remedy
 * was editing devices.json by hand and restarting the daemon. Takes effect on the device's next
 * connection.
 */
export interface DeviceForgetCmd {
  cmd: "device.forget";
  id: string;
}

/**
 * Answer a pending approval with the id of the option chosen. Anything the daemon did not offer —
 * DENY_OPTION, an unknown id, an omitted field — refuses, so there is no value that approves by
 * accident.
 */
export interface ApprovalResolve {
  cmd: "approval.resolve";
  id: string;
  option: string;
}

/** The reserved option id that refuses. Never one of an ApprovalRequest's own options. */
export const DENY_OPTION = "deny";

/**
 * Report the app's foreground/background state. While any connection is active the daemon answers
 * approvals over the WebSocket; when none is active they are pushed out of band. A fresh connection is
 * active until it says otherwise.
 */
export interface PresenceSet {
  cmd: "presence.set";
  active: boolean;
}

/** Request a workspace's connectable MCP accounts and their status (→ AuthAccounts). */
export interface AuthListCmd {
  cmd: "auth.list";
  ws: string;
}

/** Start connecting a discover-mode MCP account: the server by name, with optional scopes (→ AuthOpen,
    or an error). The daemon runs discovery + dynamic registration and returns a consent URL. */
export interface AuthBeginCmd {
  cmd: "auth.begin";
  ws: string;
  server: string;
  scopes?: string[];
}

/** Relay the intercepted authorization code back to finish the session begun by auth.begin (→ AuthDone). */
export interface AuthCallbackCmd {
  cmd: "auth.callback";
  ws: string;
  id: string;
  code: string;
  state: string;
}

/** The closed union of everything the client sends. */
export type ClientCommand =
  | ChatSubmit
  | ChatOpen
  | ChatListCmd
  | ChatCancel
  | ChatMarkRead
  | AgentListCmd
  | AgentFireCmd
  | WorkspaceListCmd
  | WorkspaceCreateCmd
  | WorkspaceRenameCmd
  | WorkspaceDeleteCmd
  | WorkspaceReloadCmd
  | SkillListCmd
  | SkillReadCmd
  | SkillEnableCmd
  | SkillRemoveCmd
  | PluginListCmd
  | MCPListCmd
  | MCPAddCmd
  | MCPRemoveCmd
  | LibraryListCmd
  | LibraryRefreshCmd
  | LibraryInstallCmd
  | JoinListCmd
  | DeviceListCmd
  | DeviceForgetCmd
  | ApprovalResolve
  | ReminderListCmd
  | ReminderCancelCmd
  | AuthListCmd
  | AuthBeginCmd
  | AuthCallbackCmd
  | PresenceSet;

// ── Pairing & Auth (HTTP, NOT the WebSocket) ─────────────────────────────────
//
// A device must pair before it can open `/ws`. Pairing yields a bearer; the app stores it (secure
// storage) and sends it on every WebSocket connection as `Authorization: Bearer <token>`, or as
// `?token=<bearer>` on the ws URL where a header can't be set on the handshake (browser). An
// unknown/absent bearer → the `/ws` upgrade is refused with HTTP 401.
//
//   • First device: the daemon logs a one-time bootstrap code while no device is paired. Redeem it
//     with POST /pair.
//   • Further devices: POST /join returns a joinId but NEVER the code; the code is shown only to an
//     already-paired device via the `join.list` event. A human reads it off the trusted device and
//     types it into the new one → POST /join/confirm.

/** POST /pair — redeem the bootstrap code for the first device. */
export interface PairRequest {
  credential: string;
  name: string;
  platform?: Platform;
}
export interface PairResponse {
  bearer: string;
}

/** POST /join — a new (unpaired) device asks to join. The response carries NO code. */
export interface JoinRequest {
  name: string;
  platform?: Platform;
}
export interface JoinResponse {
  joinId: string;
  /**
   * How many paired devices are connected right now to display the code.
   *
   * Zero is not an error — the join stays open and a device that connects later will show it — but it
   * is the difference between "read the code off your phone" and "nothing can show you a code yet",
   * and a client that cannot tell them apart leaves the user waiting on a screen nobody will look at.
   * Absent from daemons predating this field.
   */
  reachable?: number;
}

/** POST /join/confirm — submit the code read off an already-paired device. */
export interface JoinConfirmRequest {
  joinId: string;
  code: string;
}
export interface JoinConfirmResponse {
  bearer: string;
}
