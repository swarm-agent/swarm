import { SwarmTimeoutError } from './errors.js';
import type { SwarmTransport } from './transport.js';
import type {
  CreateSessionParams,
  ListSessionsParams,
  SessionDetail,
  SessionRecord,
} from './types.js';

function generateRequestId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return `sdk-session-${crypto.randomUUID()}`;
  }
  return `sdk-session-${Date.now().toString(36)}-${Math.random().toString(36).substring(2, 10)}`;
}

export class SwarmSessionsNamespace {
  private transport: SwarmTransport;

  constructor(transport: SwarmTransport) {
    this.transport = transport;
  }

  /**
   * Creates a new durable V3 session.
   */
  async create(params: CreateSessionParams): Promise<SessionRecord> {
    const clientRequestId = params.client_request_id || generateRequestId();
    const agentName = params.agent_name || params.agent || 'swarm';

    const res = await this.transport.request<{ ok: boolean; session?: SessionRecord } | SessionRecord>(
      '/v3/sessions',
      {
        method: 'POST',
        body: {
          client_request_id: clientRequestId,
          agent_name: agentName,
          title: params.title,
          workspace_id: params.workspace_id,
          workspace_path: params.workspace_path,
          mode: params.mode ?? 'auto',
          metadata: params.metadata,
        },
      }
    );

    const data = res.data;
    if (data && typeof data === 'object' && 'session' in data && data.session) {
      return data.session;
    }
    return data as SessionRecord;
  }

  /**
   * Lists durable sessions for the active account.
   */
  async list(params: ListSessionsParams = {}): Promise<SessionRecord[]> {
    const q = new URLSearchParams();
    if (params.limit && params.limit > 0) q.set('limit', params.limit.toString());
    if (params.state) q.set('state', params.state);
    if (params.workspace_id) q.set('workspace_id', params.workspace_id);
    if (params.category) q.set('category', params.category);
    if (params.cursor) q.set('cursor', params.cursor);

    const queryStr = q.toString() ? `?${q.toString()}` : '';
    const res = await this.transport.request<{ ok: boolean; sessions?: SessionRecord[] } | SessionRecord[]>(
      `/v3/sessions${queryStr}`,
      { method: 'GET' }
    );

    const data = res.data;
    if (Array.isArray(data)) {
      return data;
    }
    if (data && typeof data === 'object' && 'sessions' in data && Array.isArray(data.sessions)) {
      return data.sessions;
    }
    return [];
  }

  /**
   * Retrieves full details, history, and state for a single session.
   */
  async get(sessionId: string): Promise<SessionDetail> {
    const id = encodeURIComponent(sessionId.trim());
    const res = await this.transport.request<any>(`/v3/sessions/${id}`, {
      method: 'GET',
    });
    const data = res.data;
    if (data && typeof data === 'object') {
      const sessionObj = data.session || data;
      return {
        id: sessionObj.id || sessionObj.session_id || sessionId,
        title: sessionObj.title || '',
        workspace_id: sessionObj.workspace_id,
        workspace_path: sessionObj.workspace_path,
        state: sessionObj.state || data.projection?.state || 'idle',
        mode: sessionObj.mode,
        agent_name: sessionObj.agent_name,
        created_at: sessionObj.created_at,
        updated_at: sessionObj.updated_at,
        archived: sessionObj.archived,
        messages: data.messages,
        last_run_id: sessionObj.last_run_id,
        active_plan: data.active_plan,
        raw: data,
      };
    }
    return data as SessionDetail;
  }

  /**
   * Archives a session.
   */
  async archive(sessionId: string): Promise<boolean> {
    const id = encodeURIComponent(sessionId.trim());
    const res = await this.transport.request<{ ok: boolean }>(`/v3/sessions/${id}/archive`, {
      method: 'POST',
    });
    return res.data?.ok === true;
  }

  /**
   * Unarchives a previously archived session.
   */
  async unarchive(sessionId: string): Promise<boolean> {
    const res = await this.transport.request<{ ok: boolean }>('/v3/sessions:unarchive', {
      method: 'POST',
      body: {
        session_id: sessionId.trim(),
      },
    });
    return res.data?.ok === true;
  }

  /**
   * Deletes a session and triggers cascading cancellation of associated runs and worktrees.
   */
  async delete(sessionId: string): Promise<boolean> {
    const id = encodeURIComponent(sessionId.trim());
    const res = await this.transport.request<{ ok: boolean }>(`/v3/sessions/${id}`, {
      method: 'DELETE',
    });
    return res.data?.ok === true;
  }

  /**
   * Appends a message to a session conversation.
   */
  async sendMessage(
    sessionId: string,
    params: { content: string; role?: 'user' | 'assistant' | 'system'; client_request_id?: string }
  ): Promise<any> {
    const id = encodeURIComponent(sessionId.trim());
    const clientRequestId = params.client_request_id || generateRequestId();
    const res = await this.transport.request<any>(`/v3/sessions/${id}/messages`, {
      method: 'POST',
      body: {
        client_request_id: clientRequestId,
        content: params.content,
        role: params.role ?? 'user',
      },
    });
    return res.data;
  }

  /**
   * Stops an active execution run for a session.
   */
  async stopRun(
    sessionId: string,
    params: { run_id: string; target_swarm_id?: string }
  ): Promise<boolean> {
    const id = encodeURIComponent(sessionId.trim());
    const res = await this.transport.request<{ ok: boolean }>(`/v3/sessions/${id}/run/stop`, {
      method: 'POST',
      body: {
        run_id: params.run_id,
        target_swarm_id: params.target_swarm_id || 'self',
      },
    });
    return res.data?.ok === true;
  }

  /**
   * Helper that polls a session until its active run completes, or times out.
   */
  async waitForRun(
    sessionId: string,
    options: { timeoutMs?: number; pollIntervalMs?: number } = {}
  ): Promise<SessionDetail> {
    const timeoutMs = options.timeoutMs ?? 60_000;
    const pollIntervalMs = options.pollIntervalMs ?? 1_000;
    const deadline = Date.now() + timeoutMs;

    while (Date.now() < deadline) {
      const detail = await this.get(sessionId);
      const raw = (detail.raw || {}) as Record<string, any>;
      const activeRun = raw.active_run_intent;
      const state = (detail.state || '').toLowerCase();
      const isRunning = state.includes('running') || state.includes('in_progress') || !!activeRun;
      if (!isRunning) {
        return detail;
      }
      await new Promise((r) => setTimeout(r, pollIntervalMs));
    }

    throw new SwarmTimeoutError(`Session run did not complete within ${timeoutMs}ms`, timeoutMs);
  }
}
