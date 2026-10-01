import type { SwarmTransport } from './transport.js';
import type { SessionRecord, CreateSessionParams } from './types.js';

export interface ApplicationAgent {
  id: string;
  name: string;
  instructions: string;
  context: string;
  revision: number;
}
export type ApplicationAgentWrite = Omit<ApplicationAgent, 'id' | 'revision'> & { expected_revision: number };
export type ApplicationConversationParams = CreateSessionParams & { client_request_id: string; revision: number };
export interface ApplicationConversationSnapshot {
  session: SessionRecord;
  messages?: Array<{ id: string; role: string; content: string }>;
  [key: string]: unknown;
}

function segment(value: string): string {
  if (!value.trim() || value === '.' || value === '..' || /[/\\]/.test(value)) throw new Error('A nonempty resource id without path separators is required');
  return encodeURIComponent(value);
}

/** Application context and owned conversations; not a second execution engine. */
export class SwarmAppsNamespace {
  constructor(private readonly transport: SwarmTransport) {}
  private path(id: string): string { return `/v3/application-agents/${segment(id)}`; }

  /** Create with expected_revision=0; update with the revision last read. */
  async put(id: string, input: ApplicationAgentWrite): Promise<ApplicationAgent> {
    return (await this.transport.request<ApplicationAgent>(this.path(id), { method: 'PUT', body: input })).data;
  }
  async get(id: string): Promise<ApplicationAgent> {
    return (await this.transport.request<ApplicationAgent>(this.path(id), { method: 'GET' })).data;
  }
  /** Context is pinned for the lifetime of this conversation, including restart. */
  async createConversation(id: string, input: ApplicationConversationParams): Promise<SessionRecord> {
    const { revision, ...params } = input;
    if (!Number.isSafeInteger(revision) || revision < 1) throw new Error('A positive agent revision is required');
    const response = await this.transport.request<{ session: SessionRecord }>(`${this.path(id)}/conversations?revision=${revision}`, {
      method: 'POST', body: { ...params, agent_name: params.agent_name ?? params.agent ?? 'swarm' },
    });
    return response.data.session;
  }
  /** Reopen by durable id, with server-side agent ownership validation. */
  async conversation(id: string, sessionId: string): Promise<ApplicationConversationSnapshot> {
    return (await this.transport.request<ApplicationConversationSnapshot>(`${this.path(id)}/conversations/${segment(sessionId)}`, { method: 'GET' })).data;
  }
  /** Event IDs should be stable across delivery retries. Content is a user message, not instructions. */
  async send(id: string, sessionId: string, input: { content: string; client_request_id: string }): Promise<Record<string, unknown>> {
    return (await this.transport.request<Record<string, unknown>>(`${this.path(id)}/conversations/${segment(sessionId)}/messages`, {
      method: 'POST', body: { ...input, role: 'user' },
    })).data;
  }
}
