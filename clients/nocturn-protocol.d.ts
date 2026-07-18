/**
 * Nocturn companion-app wire protocol — TypeScript definitions.
 *
 * Mirrors internal/appserver (wire.go + state.go). Transport is a single WebSocket:
 * the client sends `ClientCommand` JSON, the server sends `ServerEvent` JSON. Every
 * message is a tagged object — discriminate on `type` (server) / `cmd` (client).
 *
 * Nothing here grants authority: chat commands run through the workspace's turn loop,
 * so the broker + human-in-the-loop still gate every effect. A remote client is no more
 * powerful than the local TUI.
 *
 * Generated from the Go source — keep in sync when the wire changes.
 */

// ── shared value types ───────────────────────────────────────────────────────

/** Who originated a turn's input. */
export type Source = "user" | "wake" | "remind" | "schedule" | "spawn";
// "schedule" = a cron firing delivered into a one-shot agent chat; "spawn" = an in-chat
// /agent invocation. (These replace the former single "agent" source.)

/** One tool call's start/end frame (the observable forest). */
export interface ToolFrame {
  id: number;
  parent: number; // enclosing call's id; 0 = root
  tool: string;
  args?: string; // JSON, as supplied
  phase: "start" | "end";
  result?: string; // end only
  err?: string; // end only (e.g. a denied effect)
}

/** One buffered input in a snapshot. */
export interface QueuedItem {
  display: string;
  input: string;
  source: Source;
}

/**
 * One finished tool call reconstructed from history for a snapshot — no id/parent/phase
 * (that's the live `ToolFrame`); just what ran and what it returned. Render as a static
 * forest under the assistant bubble.
 */
export interface SnapshotTool {
  tool: string;
  args?: string; // JSON, as supplied
  result?: string;
}

/**
 * One conversation message in a snapshot (user/assistant only). An assistant turn that only
 * called tools has empty `content` and a non-empty `tools` — render the forest, not a bubble.
 */
export interface Message {
  role: "user" | "assistant";
  content: string;
  tools?: SnapshotTool[]; // assistant turn: the tool calls it made
}

/** A child agent, for the app's agent list. */
export interface AgentInfo {
  name: string;
  description: string;
}

/** A discovered skill. */
export interface SkillInfo {
  name: string;
  description: string;
}

/** An installed plugin and the tools it exposes. */
export interface PluginInfo {
  name: string;
  tools: string[];
}

/** List-view state of one workspace (the picker). */
export interface WorkspaceSummary {
  name: string;
  running: boolean;
  agents: number;
  skills: number;
  personaSet: boolean;
}

/** Detail-view state of one workspace. `accounts` are presence names, never secret values. */
export interface WorkspaceState {
  name: string;
  persona: string;
  agents: AgentInfo[];
  skills: SkillInfo[];
  plugins: PluginInfo[];
  accounts: string[];
}

/** One chat's summary in a workspace's chat list. A workspace holds several named chats. */
export interface ChatMeta {
  id: string;
  name: string;
  origin: "user" | "agent"; // who created it — filter/group human chats vs agent activity
  agent?: string; // the owning agent's name for an agent run ("" / absent = a user chat) — group runs per agent
  updated: string; // RFC3339
  turns: number; // user messages, for an "N messages" hint
}

// ── server → client events ───────────────────────────────────────────────────

/** One streamed chunk of the assistant's answer. */
export interface TokenEvent {
  type: "token";
  text: string;
}

/** One streamed chunk of the model's reasoning (render it dim). */
export interface ThinkingEvent {
  type: "thinking";
  text: string;
}

/** A turn beginning. `display` is what to render; `input` is what actually ran. */
export interface TurnStartEvent {
  type: "turnStart";
  display: string;
  input: string;
  source: Source;
}

/** A turn finishing, with its final answer or error. */
export interface TurnEndEvent {
  type: "turnEnd";
  answer?: string;
  err?: string;
}

/** An input buffered while a turn was running (type-ahead, or a wake/remind). */
export interface QueuedEvent {
  type: "queued";
  display: string;
  input: string;
  source: Source;
}

/** A dim system line (reset, a background scheduler line). */
export interface NoticeEvent {
  type: "notice";
  text: string;
  isErr?: boolean;
}

/** A tool call's start or end. */
export interface ToolEvent {
  type: "tool";
  tool: ToolFrame;
}

/**
 * An approval request out of band: render `intent` + the choice `options`, answer with a
 * `resolve` command carrying the chosen index. `options` are LABELS only — the signed
 * tokens stay host-side. Convention: send `choice = -1` to DENY (e.g. a cancel button).
 */
export interface ApprovalEvent {
  type: "approval";
  id: string;
  intent: string;
  options: string[];
}

/** A pending approval was answered (possibly on another device) — clear the prompt. */
export interface ApprovalResolvedEvent {
  type: "approvalResolved";
  id: string;
}

/** Sent on connect and on every workspace switch — the state to render current. */
export interface SnapshotEvent {
  type: "snapshot";
  running: boolean;
  queue: QueuedItem[];
  messages: Message[];
  pending?: ApprovalEvent; // an unanswered approval, or absent
}

/** Reply to `listWorkspaces` — the picker. */
export interface WorkspacesEvent {
  type: "workspaces";
  items: WorkspaceSummary[];
}

/** Reply to `getWorkspace` / `setPersona` — one workspace's detail. */
export interface WorkspaceEvent extends WorkspaceState {
  type: "workspace";
}

/** Reply to `listChats` / `newChat` / `renameChat` / `deleteChat` — a workspace's chat list. */
export interface ChatsEvent {
  type: "chats";
  ws: string;
  items: ChatMeta[];
}

/**
 * A lightweight badge signal for a chat the client may NOT have open: it finished a turn
 * (`turnEnd` — badge it) or is waiting on an approval (`approvalPending` — actionable). No
 * conversation content; refresh the real state with `listChats` / `openChat`. The currently
 * open chat also streams its full events, so ignore activity for the open chat's id.
 */
export interface ChatActivityEvent {
  type: "chatActivity";
  ws: string;
  id: string;
  kind: "turnEnd" | "approvalPending";
}

/** A control error (e.g. unknown workspace). */
export interface ErrorEvent {
  type: "error";
  text: string;
}

/** The closed union of everything the server sends. Switch on `type`. */
export type ServerEvent =
  | TokenEvent
  | ThinkingEvent
  | TurnStartEvent
  | TurnEndEvent
  | QueuedEvent
  | NoticeEvent
  | ToolEvent
  | ApprovalEvent
  | ApprovalResolvedEvent
  | SnapshotEvent
  | WorkspacesEvent
  | WorkspaceEvent
  | ChatsEvent
  | ChatActivityEvent
  | ErrorEvent;

// ── client → server commands ─────────────────────────────────────────────────

/** Send a user message as a turn. */
export interface SubmitCommand {
  cmd: "submit";
  input: string;
}

/** Activate a skill: `display` is the typed "/name" line, `input` is the expanded body. */
export interface SubmitSkillCommand {
  cmd: "submitSkill";
  display: string;
  input: string;
}

/** Run a named child agent: `display` is the typed line, `task` is its input. */
export interface SubmitAgentCommand {
  cmd: "submitAgent";
  display: string;
  agent: string;
  task: string;
}

/** Cancel the running turn. */
export interface CancelCommand {
  cmd: "cancel";
}

/** End the session and start fresh (revoke session grants, clear history). */
export interface ResetCommand {
  cmd: "reset";
}

/** Answer the pending approval: `choice` is the option index, or -1 to deny. */
export interface ResolveCommand {
  cmd: "resolve";
  id: string;
  choice: number;
}

/** List all workspaces (→ WorkspacesEvent). */
export interface ListWorkspacesCommand {
  cmd: "listWorkspaces";
}

/** Get one workspace's detail (→ WorkspaceEvent). */
export interface GetWorkspaceCommand {
  cmd: "getWorkspace";
  ws: string;
}

/** Set a workspace's persona (→ WorkspaceEvent echo of the new state). */
export interface SetPersonaCommand {
  cmd: "setPersona";
  ws: string;
  text: string;
}

/** List a workspace's chats (→ ChatsEvent). */
export interface ListChatsCommand {
  cmd: "listChats";
  ws: string;
}

/** Create a new empty chat in a workspace (→ ChatsEvent with the updated list). */
export interface NewChatCommand {
  cmd: "newChat";
  ws: string;
  name: string;
}

/** Open a chat: subscribes to its stream (→ SnapshotEvent then events). */
export interface OpenChatCommand {
  cmd: "openChat";
  ws: string;
  id: string;
}

/** Rename a chat (→ ChatsEvent with the updated list). */
export interface RenameChatCommand {
  cmd: "renameChat";
  ws: string;
  id: string;
  name: string;
}

/** Delete a chat and stop its runner (→ ChatsEvent with the updated list). */
export interface DeleteChatCommand {
  cmd: "deleteChat";
  ws: string;
  id: string;
}

/** The closed union of everything the client sends. */
export type ClientCommand =
  | SubmitCommand
  | SubmitSkillCommand
  | SubmitAgentCommand
  | CancelCommand
  | ResetCommand
  | ResolveCommand
  | ListWorkspacesCommand
  | GetWorkspaceCommand
  | SetPersonaCommand
  | ListChatsCommand
  | NewChatCommand
  | OpenChatCommand
  | RenameChatCommand
  | DeleteChatCommand;
