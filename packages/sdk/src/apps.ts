import { watchApplicationResults } from './app-results.js';
import type { ApplicationResultsOptions } from './app-results.js';
import { SwarmRealtimeNamespace } from './realtime.js';
import type { SessionWatchOptions, SessionWatch } from './realtime.js';
import type { WorkerAutomationInput, TriggerWorkerParams } from './types.js';
import type { SwarmTransport } from './transport.js';
import type { SessionRecord, CreateSessionParams, CreateProjectTaskParams, ProjectTaskRecord, WorkerRecord, WorkerRunRecord } from './types.js';

export interface ApplicationAgent {
  id: string;
  name: string;
  instructions: string;
  context: string;
  revision: number;
  project_id?: string;
  worker_ids?: string[];
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
  watchResults(id: string, options: ApplicationResultsOptions): SessionWatch {
    segment(id);
    return watchApplicationResults(this, this.transport, id, options);
  }
  private path(id: string): string { return `/v3/application-agents/${segment(id)}`; }

  async list(params: { limit?: number; cursor?: string } = {}): Promise<{ agents: ApplicationAgent[]; next_cursor?: string }> {
    const q = new URLSearchParams();
    if (params.limit !== undefined) q.set('limit', String(params.limit));
    if (params.cursor) q.set('cursor', params.cursor);
    return (await this.transport.request<{ agents: ApplicationAgent[]; next_cursor?: string }>(`/v3/application-agents?${q}`, { method: 'GET' })).data;
  }
  async tasks(id: string, signal?: AbortSignal): Promise<{ tasks: ProjectTaskRecord[] }> {
    return (await this.transport.request<{ tasks: ProjectTaskRecord[] }>(`${this.path(id)}/tasks`, { method: 'GET', signal })).data;
  }
  async createTask(id: string, input: CreateProjectTaskParams): Promise<{ task: ProjectTaskRecord }> {
    return (await this.transport.request<{ task: ProjectTaskRecord }>(`${this.path(id)}/tasks`, { method: 'POST', body: input })).data;
  }
  async worker(id: string, workerId: string, signal?: AbortSignal): Promise<{ worker: WorkerRecord }> {
    return (await this.transport.request<{ worker: WorkerRecord }>(`${this.path(id)}/workers/${segment(workerId)}`, { method: 'GET', signal })).data;
  }
  async runs(id: string, workerId: string, signal?: AbortSignal): Promise<{ runs: WorkerRunRecord[] }> {
    return (await this.transport.request<{ runs: WorkerRunRecord[] }>(`${this.path(id)}/workers/${segment(workerId)}/runs`, { method: 'GET', signal })).data;
  }
  /** Configuration changes retain canonical worker review/approval gates. */
  async configureAutomation(id: string, workerId: string, expectedRevision: number, automation: WorkerAutomationInput, automationId?: string): Promise<{ worker: WorkerRecord }> {
    if (!Number.isSafeInteger(expectedRevision) || expectedRevision < 1) throw new Error('A positive worker revision is required');
    const path = `${this.path(id)}/workers/${segment(workerId)}/automations${automationId === undefined ? '' : `/${segment(automationId)}`}`;
    return (await this.transport.request<{ worker: WorkerRecord }>(path, { method: automationId === undefined ? 'POST' : 'PUT', body: { expected_worker_revision: expectedRevision, automation } })).data;
  }
  async trigger(id: string, input: TriggerWorkerParams & { idempotency_key: string }): Promise<Record<string, unknown>> {
    if (!input.idempotency_key.trim()) throw new Error('A stable delivery id is required');
    const suffix = input.automation_id === undefined ? '/trigger' : `/automations/${segment(input.automation_id)}/trigger`;
    return (await this.transport.request<Record<string, unknown>>(`${this.path(id)}/workers/${segment(input.worker_id)}${suffix}`, {
      method: 'POST', body: { payload: input.payload, idempotency_key: input.idempotency_key },
    })).data;
  }
  /** Ownership is checked before opening the canonical V3 session stream. */
  async watchConversation(id: string, sessionId: string, options: SessionWatchOptions): Promise<SessionWatch> {
    await this.conversation(id, sessionId);
    return new SwarmRealtimeNamespace(this.transport).watchSession(sessionId, options);
  }
  /** Create with expected_revision=0; update with the revision last read. */
  async put(id: string, input: ApplicationAgentWrite): Promise<ApplicationAgent> {
    return (await this.transport.request<ApplicationAgent>(this.path(id), { method: 'PUT', body: input })).data;
  }
  async get(id: string, signal?: AbortSignal): Promise<ApplicationAgent> {
    return (await this.transport.request<ApplicationAgent>(this.path(id), { method: 'GET', signal })).data;
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
  async conversations(id: string): Promise<{ sessions: SessionRecord[]; scan_limit: number; scan_limit_reached: boolean }> {
    return (await this.transport.request<{ sessions: SessionRecord[]; scan_limit: number; scan_limit_reached: boolean }>(`${this.path(id)}/conversations`, { method: 'GET' })).data;
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
