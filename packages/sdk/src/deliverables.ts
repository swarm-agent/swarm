import type { SwarmTransport } from './transport.js';
import type {
  CreateDeliverableParams,
  DeliverableFilter,
  DeliverableRecord,
} from './types.js';

export class SwarmDeliverablesNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  /**
   * Submits a new deliverable (e.g. social post batch, alert, report, PR draft) to the Agent Mailbox.
   */
  async submit(params: CreateDeliverableParams): Promise<DeliverableRecord> {
    const res = await this.transport.request<{ deliverable: DeliverableRecord }>(
      '/v3/deliverables',
      {
        method: 'POST',
        body: params,
      }
    );
    return res.data.deliverable;
  }

  /**
   * Lists deliverables across the account, optionally filtered by status, worker, kind, or workspace.
   */
  async list(filter: DeliverableFilter = {}): Promise<DeliverableRecord[]> {
    const q = new URLSearchParams();
    if (filter.status) q.set('status', filter.status);
    if (filter.worker_id) q.set('worker_id', filter.worker_id);
    if (filter.kind) q.set('kind', filter.kind);
    if (filter.workspace_id) q.set('workspace_id', filter.workspace_id);
    if (typeof filter.limit === 'number') q.set('limit', String(filter.limit));

    const path = q.toString() ? `/v3/deliverables?${q.toString()}` : '/v3/deliverables';
    const res = await this.transport.request<{ deliverables?: DeliverableRecord[]; count: number }>(
      path,
      {
        method: 'GET',
      }
    );
    return res.data.deliverables || [];
  }

  /**
   * Fetches a single deliverable by ID.
   */
  async get(id: string): Promise<DeliverableRecord> {
    const res = await this.transport.request<{ deliverable: DeliverableRecord }>(
      `/v3/deliverables/${encodeURIComponent(id)}`,
      {
        method: 'GET',
      }
    );
    return res.data.deliverable;
  }

  /**
   * Approves a deliverable, triggering any linked publication action (e.g. publish to X, webhook).
   */
  async approve(
    id: string,
    params: { parameters?: Record<string, unknown> } = {}
  ): Promise<{ deliverable: DeliverableRecord; action_result?: Record<string, unknown> }> {
    const res = await this.transport.request<{ deliverable: DeliverableRecord; action_result?: Record<string, unknown> }>(
      `/v3/deliverables/${encodeURIComponent(id)}/approve`,
      {
        method: 'POST',
        body: params,
      }
    );
    return res.data;
  }

  /**
   * Dismisses a pending deliverable without executing its publication action.
   */
  async dismiss(id: string): Promise<DeliverableRecord> {
    const res = await this.transport.request<{ deliverable: DeliverableRecord }>(
      `/v3/deliverables/${encodeURIComponent(id)}/dismiss`,
      {
        method: 'POST',
      }
    );
    return res.data.deliverable;
  }

  /**
   * Deletes a deliverable permanently from the mailbox.
   */
  async delete(id: string): Promise<void> {
    await this.transport.request<{ ok: boolean; deleted_id: string }>(
      `/v3/deliverables/${encodeURIComponent(id)}`,
      {
        method: 'DELETE',
      }
    );
  }
}
