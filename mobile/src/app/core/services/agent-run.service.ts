import { Injectable } from '@angular/core';
import type { Source } from '../protocol/nocturn-protocol';
import { ConversationService } from './conversation.service';

/**
 * AgentRunService is the AGENT-run conversation: the ConversationService bound to kind "agent", so
 * every command it sends targets the agent store. Runs are server-created (a scheduled or manual
 * firing), so there is no composer/mint flow — only open/submit/cancel on an existing run. The reused
 * ChatPage injects this (vs ChatService) by route, so an agent run opens, streams and renders exactly
 * like a user chat.
 */
@Injectable({ providedIn: 'root' })
export class AgentRunService extends ConversationService {
  protected readonly kind: Source = 'agent';
}
