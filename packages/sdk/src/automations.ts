import type { SwarmTransport } from './transport.js';
import type {
  AutomationV2ListParams,
  AutomationV2Progress,
  AutomationV2Record,
  AutomationV2TriggerParams,
  AutomationV2TriggerResult,
} from './types.js';

export class SwarmAutomationsNamespace {
  private transport: SwarmTransport;

  constructor(transport: SwarmTransport) {
    this.transport = transport;
  }

  /**
   * Immediately triggers an on-demand worker execution with caller-supplied context.
   * Admitted occurrences execute in an isolated Git worktree with runtime context
   * injected into the agent's goal and execution metadata.
   */
  async trigger(params: AutomationV2TriggerParams): Promise<AutomationV2TriggerResult> {
    const res = await this.transport.request<AutomationV2TriggerResult>('/v3/automations/v2/trigger', {
      method: 'POST',
      body: {
        workspace_id: params.workspace_id,
        worker_id: params.worker_id,
        session_id: params.session_id,
        automation_id: params.automation_id,
        context: params.context ?? {},
      },
    });
    return res.data;
  }

  /**
   * Lists all automations/workers in the specified workspace.
   */
  async list(params: AutomationV2ListParams): Promise<AutomationV2Record[]> {
    const q = new URLSearchParams();
    q.set('workspace_id', params.workspace_id);
    q.set('action', 'list');
    if (params.archived_mode) q.set('archived_mode', params.archived_mode);
    if (params.cursor) q.set('cursor', params.cursor);
    if (params.limit) q.set('limit', params.limit.toString());

    const res = await this.transport.request<{ records?: AutomationV2Record[] }>(
      `/v3/automations/v2?${q.toString()}`,
      { method: 'GET' }
    );
    return res.data?.records ?? [];
  }

  /**
   * Retrieves an automation review/definition for a given worker_id or session_id.
   */
  async get(params: { workspace_id: string; worker_id?: string; session_id?: string }): Promise<AutomationV2Record | null> {
    const q = new URLSearchParams();
    q.set('workspace_id', params.workspace_id);
    if (params.worker_id) q.set('worker_id', params.worker_id);
    if (params.session_id) q.set('session_id', params.session_id);

    const res = await this.transport.request<{ record?: AutomationV2Record }>(
      `/v3/automations/v2/review?${q.toString()}`,
      { method: 'GET' }
    );
    return res.data?.record ?? null;
  }

  /**
   * Fetches the current progress, execution history, and upcoming forecast for a worker.
   */
  async getProgress(params: {
    workspace_id: string;
    worker_id?: string;
    session_id?: string;
    timezone?: string;
    cursor?: string;
  }): Promise<AutomationV2Progress> {
    const q = new URLSearchParams();
    q.set('workspace_id', params.workspace_id);
    if (params.worker_id) q.set('worker_id', params.worker_id);
    if (params.session_id) q.set('session_id', params.session_id);
    if (params.timezone) q.set('timezone', params.timezone);
    if (params.cursor) q.set('cursor', params.cursor);

    const res = await this.transport.request<AutomationV2Progress>(
      `/v3/automations/v2/progress?${q.toString()}`,
      { method: 'GET' }
    );
    return res.data;
  }

  /**
   * Deletes an automation permanently, cancelling pending runs and purging accepted records.
   */
  async delete(params: {
    workspace_id: string;
    session_id: string;
    generation?: number;
  }): Promise<boolean> {
    const res = await this.transport.request<{ ok?: boolean; record?: unknown }>(
      '/v3/automations/v2/control',
      {
        method: 'POST',
        body: {
          action: 'delete_automation',
          workspace_id: params.workspace_id,
          session_id: params.session_id,
          generation: params.generation ?? 1,
        },
      }
    );
    return Boolean(res.data);
  }

  /**
   * Pauses an active automation schedule.
   */
  async pause(params: { workspace_id: string; session_id: string; generation: number }): Promise<boolean> {
    const res = await this.transport.request<{ ok: boolean }>('/v3/automations/v2/control', {
      method: 'POST',
      body: {
        action: 'pause',
        workspace_id: params.workspace_id,
        session_id: params.session_id,
        generation: params.generation,
      },
    });
    return res.data?.ok === true;
  }

  /**
   * Resumes a paused automation schedule.
   */
  async resume(params: { workspace_id: string; session_id: string; generation: number }): Promise<boolean> {
    const res = await this.transport.request<{ ok: boolean }>('/v3/automations/v2/control', {
      method: 'POST',
      body: {
        action: 'resume',
        workspace_id: params.workspace_id,
        session_id: params.session_id,
        generation: params.generation,
      },
    });
    return res.data?.ok === true;
  }
}
