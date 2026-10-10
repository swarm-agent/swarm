import { SwarmTimeoutError } from './errors.js';
import { SwarmPermissionsNamespace, isAskUserPermission } from './permissions.js';
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
          project_id: params.project_id,
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
    if (params.project_id) q.set('project_id', params.project_id);
    if (params.workspace_id) q.set('workspace_id', params.workspace_id);
    if (params.category) q.set('category', params.category);
    if (params.cursor) q.set('cursor', params.cursor);

    const queryStr = q.toString() ? `?${q.toString()}` : '';
    const res = await this.transport.request<{ ok: boolean; sessions?: SessionRecord[] } | SessionRecord[]>(
      `/v3/sessions${queryStr}`,
      { method: 'GET' }
    );

    const data = res.data;
    let list: any[] = [];
    if (Array.isArray(data)) {
      list = data;
    } else if (data && typeof data === 'object' && 'sessions' in data && Array.isArray(data.sessions)) {
      list = data.sessions;
    }

    return list.map((item: any) => {
      if (item && item.session && typeof item.session === 'object') {
        return {
          ...item.session,
          projection: item.projection,
        };
      }
      return item;
    });
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
      // V3 sessions carry their source workspace as the primary grant.
      const primaryGrant = Array.isArray(sessionObj.workspace_grants)
        ? sessionObj.workspace_grants.find((grant: any) => grant?.kind === 'primary')
        : undefined;
      return {
        id: sessionObj.id || sessionObj.session_id || sessionId,
        title: sessionObj.title || '',
        workspace_id: sessionObj.workspace_id || primaryGrant?.workspace_id,
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

  /** Approve one exact pending tool call; no persistent rule or bypass is created. */
  async approvePermissionOnce(sessionId: string, permissionId: string, reason?: string): Promise<{ ok: boolean }> {
    const res = await this.transport.request<{ ok: boolean }>(
      `/v3/sessions/${encodeURIComponent(sessionId.trim())}/permissions/${encodeURIComponent(permissionId.trim())}/resolve`,
      { method: 'POST', body: { action: 'allow_once', reason } }
    );
    return res.data;
  }

  /** Resolve one exact pending tool permission; never changes account-wide policy. */
  async denyPermission(sessionId: string, permissionId: string, reason?: string): Promise<{ ok: boolean }> {
    const res = await this.transport.request<{ ok: boolean }>(
      `/v3/sessions/${encodeURIComponent(sessionId.trim())}/permissions/${encodeURIComponent(permissionId.trim())}/resolve`,
      { method: 'POST', body: { action: 'deny', reason } }
    );
    return res.data;
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
    params: { content: string; role?: 'user'; client_request_id?: string }
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
    let target = params.target_swarm_id?.trim();
    if (!target) {
      const session = await this.get(sessionId);
      const rawSession = session.raw?.session as { metadata?: Record<string, unknown> } | undefined;
      const runtime = rawSession?.metadata?.swarm_v3_runtime_swarm_id;
      target = typeof runtime === 'string' ? runtime.trim() : undefined;
      if (!target) throw new Error('Session has no authoritative runtime identity; supply target_swarm_id');
    }
    const res = await this.transport.request<{ ok: boolean }>(`/v3/sessions/${id}/run/stop`, {
      method: 'POST',
      body: {
        run_id: params.run_id,
        target_swarm_id: target,
      },
    });
    return res.data?.ok === true;
  }

  /**
   * Helper that polls a session until its active run completes, or times out.
   * Explicit autoApprovePermissions applies only to ordinary tools; questions still require user input.
   */
  async waitForRun(
    sessionId: string,
    options: { timeoutMs?: number; pollIntervalMs?: number; autoApprovePermissions?: boolean } = {}
  ): Promise<SessionDetail> {
    const timeoutMs = options.timeoutMs ?? 180_000;
    const pollIntervalMs = options.pollIntervalMs ?? 1_000;
    const deadline = Date.now() + timeoutMs;
    const permissions = new SwarmPermissionsNamespace(this.transport);
    let lastPendingPerms: Array<{ id: string; tool_name: string }> = [];

    while (Date.now() < deadline) {
      lastPendingPerms = await permissions.listSessionPending(sessionId, 20);
      if (options.autoApprovePermissions) {
        for (const p of lastPendingPerms) {
          if (!isAskUserPermission(p)) await permissions.resolve(sessionId, p.id, 'allow_once', { reason: 'Approved by explicit application policy' });
        }
      }

      const detail = await this.get(sessionId);
      const raw = (detail.raw || {}) as Record<string, any>;
      const activeRun = raw.active_run_intent;
      const state = (detail.state || '').toLowerCase();
      const isRunning = state.includes('running') || state.includes('in_progress') || !!activeRun;
      if (!isRunning && lastPendingPerms.length === 0) {
        return detail;
      }
      await new Promise((r) => setTimeout(r, pollIntervalMs));
    }

    let msg = `Session run did not complete within ${timeoutMs}ms`;
    if (lastPendingPerms.length > 0) {
      msg += `. Session has ${lastPendingPerms.length} pending tool permission(s) requiring approval: ${lastPendingPerms.map((p) => p.tool_name).join(', ')}. Render the requests and explicitly answer or deny via client.permissions.resolve().`;
    }
    throw new SwarmTimeoutError(msg, timeoutMs);
  }
}
