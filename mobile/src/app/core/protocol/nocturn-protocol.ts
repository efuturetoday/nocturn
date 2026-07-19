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
 * Before the WebSocket, a client must PAIR to obtain a bearer (see "Pairing & Auth" at the
 * bottom): every `/ws` connection carries `Authorization: Bearer <token>`.
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

/**
 * One pending reminder in a workspace's reminder list. Read-only for the app — the model
 * sets/cancels reminders via its gated tools; the app only views them (pushed live on change).
 */
export interface ReminderMeta {
  id: string;
  fireAt: string; // RFC3339 — when it fires
  message: string;
  title?: string;
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

/**
 * One completed tool invocation from the persisted forest, for faithful reload. `id`/`parent`
 * give the call tree — `parent === 0` is a top-level (model-issued) call; a nested effect
 * (e.g. an http.write inside code.run) carries its enclosing call's `id`. Rebuild the tree by
 * grouping on `parent`; this is the SAME data the live `ToolFrame` stream carried, so a reload
 * shows sub-calls and their errors, not just the flat top-level calls in `Message.tools`.
 */
export interface ForestFrame {
  id: number;
  parent: number;
  tool: string;
  args?: string; // JSON, as supplied
  result?: string;
  err?: string; // a failed call (e.g. a denied effect)
}

/** Sent on connect and on every workspace switch — the state to render current. */
export interface SnapshotEvent {
  type: "snapshot";
  running: boolean;
  queue: QueuedItem[];
  messages: Message[];
  forest?: ForestFrame[]; // the full completed tool tree (sub-calls + errors), in stream order
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
 * A workspace's pending-reminder list. Sent as a reply to `listReminders` and pushed
 * unsolicited whenever the list changes (a reminder set / fired / cancelled). Full list —
 * just `set(items)`.
 */
export interface RemindersEvent {
  type: "reminders";
  ws: string;
  items: ReminderMeta[];
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

/**
 * The pending device-joins (with codes), for already-paired ("admin") devices. Sent as a reply
 * to `listJoins` and pushed unsolicited whenever a join is created/redeemed/expires. Full list —
 * just `set(items)`. Only paired (authed) connections ever receive it, so the codes are safe here.
 */
export interface JoinsEvent {
  type: "joins";
  items: PendingJoin[];
}

/**
 * The paired-device list, for device management. Reply to `listDevices` and pushed unsolicited
 * whenever the set changes (pair / revoke / push-token register). Full list — just `set(items)`.
 */
export interface DevicesEvent {
  type: "devices";
  items: DeviceMeta[];
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
  | RemindersEvent
  | JoinsEvent
  | DevicesEvent
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

/** List a workspace's pending reminders (→ RemindersEvent; changes then arrive live). */
export interface ListRemindersCommand {
  cmd: "listReminders";
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

/**
 * Report the app's foreground/background state. Send `active:true` when the app comes to the
 * foreground, `active:false` when it backgrounds. It drives out-of-band routing: while ANY
 * connection is active the daemon answers approvals over the WebSocket (in-band prompt or an
 * `approvalPending` badge); when none is active a background approval is pushed to the phone.
 * A fresh connection is assumed active until it says otherwise.
 */
export interface SetPresenceCommand {
  cmd: "setPresence";
  active: boolean;
}

/** List the pending device-joins (→ JoinsEvent; changes then arrive live). Bearer-implied. */
export interface ListJoinsCommand {
  cmd: "listJoins";
}

/** List the paired devices (→ DevicesEvent; changes then arrive live). */
export interface ListDevicesCommand {
  cmd: "listDevices";
}

/** Unpair a device by id (→ a fresh DevicesEvent is pushed). Its bearer stops working next connect. */
export interface RevokeDeviceCommand {
  cmd: "revokeDevice";
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
  | ListRemindersCommand
  | NewChatCommand
  | OpenChatCommand
  | RenameChatCommand
  | DeleteChatCommand
  | SetPresenceCommand
  | ListJoinsCommand
  | ListDevicesCommand
  | RevokeDeviceCommand;

// ── Pairing & Auth (HTTP, NOT the WebSocket) ─────────────────────────────────
//
// A device must pair before it can open `/ws`. Pairing yields a bearer; the app stores it
// (secure storage) and sends it on EVERY WebSocket connection as `Authorization: Bearer
// <token>` (or, where a header can't be set on the handshake, `?token=<bearer>` on the ws URL).
// An unknown/absent bearer → the `/ws` upgrade is refused with HTTP 401.
//
// The daemon runs headless (the TUI may not exist), so the trust root is out-of-band:
//   • Bootstrap (first device): `nocturn serve` with zero paired devices prints a QR + a
//     6-digit OTP to its stdout. The QR encodes `nocturn://pair?host=<ip>&port=<p>&secret=<32B>`.
//     Scan it (→ POST /pair with the secret) or type the OTP (→ POST /pair with the OTP).
//   • Second+ device: the new device calls POST /join{name,platform} and gets a joinId (NO
//     code). The code is revealed only to already-paired devices via the `joins` WS event
//     (below). A human reads it off a trusted (already-paired) screen and TYPES it into the new
//     device → POST /join/confirm.
//
// All endpoints answer a CORS preflight (OPTIONS → 204). Bodies are JSON. Errors are plain-text
// HTTP 4xx (401 for a bad/expired/exhausted credential, 400 for malformed input).

/**
 * A device platform, selecting the push provider (ios→APNs, android→FCM). The app SHOULD send it
 * (e.g. Capacitor.getPlatform()); if omitted, the daemon infers it from the User-Agent, defaulting
 * to "web". So sending it is optional but recommended — a wrong/absent value degrades push routing.
 */
export type Platform = "ios" | "android" | "web";

/** POST /pair — redeem the bootstrap QR secret OR the typed OTP. `name` labels the device. */
export interface PairRequest {
  credential: string; // the QR's `secret`, or the 6-digit OTP
  name: string;
  platform?: Platform; // the device's OS — recorded now so push registration only needs the token
}
export interface PairResponse {
  bearer: string; // store securely; send on every /ws connection
}

/** POST /join — a new (unpaired) device asks to join. The response carries NO code. */
export interface JoinRequest {
  name: string;
  platform?: Platform;
}
export interface JoinResponse {
  joinId: string; // confirm with this; the code arrives on a paired device via the `joins` event
}

/** POST /join/confirm — submit the code the user read off an already-paired device. */
export interface JoinConfirmRequest {
  joinId: string;
  code: string; // the 6-digit code shown by the `joins` event on a paired device
}
export interface JoinConfirmResponse {
  bearer: string;
}

/**
 * POST /register — bearer-gated. The app registers its native push token (from the OS, after the
 * user grants push permission) so the daemon can wake it out-of-band for a background approval.
 * An empty token clears it (the user revoked push). Responds 204.
 */
export interface RegisterRequest {
  token: string; // APNs/FCM device token; "" to unregister
  platform?: Platform;
}

/**
 * One pending device-join, carried by the `joins` WS event to already-paired ("admin") devices:
 * the code a human relays to the joining device. Never sent to the joining (unpaired) device.
 */
export interface PendingJoin {
  joinId: string;
  name: string;
  code: string; // show this so the user can type it into the joining device
  platform?: Platform;
}

/**
 * One PAIRED device for the device-management view. Carries NO secret — never the bearer or its
 * hash. `hasPush` is whether a push token is registered (out-of-band reachable). Fetch with
 * `listDevices`; pushed live via the `devices` event; unpair with `revokeDevice`.
 */
export interface DeviceMeta {
  id: string; // the stable handle used by revokeDevice
  name: string;
  platform?: Platform;
  added: string; // RFC3339
  lastUsed?: string; // RFC3339 — absent if never used since pairing
  hasPush: boolean;
  self?: boolean; // true for the requesting connection's OWN device — render "this device" / sign-out
}
