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

/** One workspace (an isolated stack of chats/tools/grants) the daemon serves. */
export interface WorkspaceInfo {
  name: string;
}

/** One chat's summary (chat.list). The name is derived from the first message. */
export interface ChatMeta {
  id: string;
  name: string;
  source: Source;
  created: string; // RFC3339
  updated: string; // RFC3339
  read?: string; // RFC3339 shared read cursor; unread when updated > read (absent = never read)
  turns: number;
  preview?: string; // last message's first line — the list row's subtitle (à la Apple Mail)
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


/** A chat's persisted transcript, sent on chat.open so the client can render it. */
export interface ChatSnapshot {
  type: "chat.snapshot";
  id: string;
  messages: Message[];
}

/** A workspace's chat list, replying to chat.list. */
export interface ChatList {
  type: "chat.list";
  ws: string;
  chats: ChatMeta[];
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

/**
 * An out-of-band approval request: render `intent` + the choice `options` (labels), answer with an
 * `approval.resolve` carrying the chosen index, or -1 to deny.
 */
export interface ApprovalRequest {
  type: "approval.request";
  id: string;
  frame?: number; // the tool call this approval is for (freeze that tool's timer); absent = not tool-scoped
  intent: string;
  options: string[];
}

/** A pending approval concluded (answered here or elsewhere, timed out, or cancelled) — clear the prompt. */
export interface ApprovalResolved {
  type: "approval.resolved";
  id: string;
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
  | WorkspaceList
  | ChatActivity
  | JoinList
  | ApprovalRequest
  | ApprovalResolved
  | ErrorEvent;

// ── client → server commands (discriminate on `cmd`) ─────────────────────────

/** Send a message to chat `id` (client-minted). An unknown id starts that chat, a known one appends. */
export interface ChatSubmit {
  cmd: "chat.submit";
  ws: string;
  text: string;
  id: string;
}

/** Resume a chat in workspace `ws`: the server replies with a ChatSnapshot, then streams its turns. */
export interface ChatOpen {
  cmd: "chat.open";
  ws: string;
  id: string;
}

/** Request a workspace's chat list (→ ChatList). */
export interface ChatListCmd {
  cmd: "chat.list";
  ws: string;
}

/** Abort a chat's running turn (id-addressed; the chat and session stay open). */
export interface ChatCancel {
  cmd: "chat.cancel";
  ws: string;
  id: string;
}

/** Mark a chat read up to its latest turn — the daemon advances a shared cursor for every device. */
export interface ChatMarkRead {
  cmd: "chat.markRead";
  ws: string;
  id: string;
}

/** Request the daemon's workspaces (→ WorkspaceList). */
export interface WorkspaceListCmd {
  cmd: "workspace.list";
}

/** Request the pending second-device joins with codes (→ JoinList). */
export interface JoinListCmd {
  cmd: "join.list";
}

/** Answer a pending approval: the chosen option index, or -1 to deny. */
export interface ApprovalResolve {
  cmd: "approval.resolve";
  id: string;
  choice: number;
}

/**
 * Report the app's foreground/background state. While any connection is active the daemon answers
 * approvals over the WebSocket; when none is active they are pushed out of band. A fresh connection is
 * active until it says otherwise.
 */
export interface PresenceSet {
  cmd: "presence.set";
  active: boolean;
}

/** The closed union of everything the client sends. */
export type ClientCommand =
  | ChatSubmit
  | ChatOpen
  | ChatListCmd
  | ChatCancel
  | ChatMarkRead
  | WorkspaceListCmd
  | JoinListCmd
  | ApprovalResolve
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
}

/** POST /join/confirm — submit the code read off an already-paired device. */
export interface JoinConfirmRequest {
  joinId: string;
  code: string;
}
export interface JoinConfirmResponse {
  bearer: string;
}
