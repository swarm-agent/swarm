import type { SwarmTransport } from './transport.js';
import { SwarmValidationError } from './errors.js';

export interface UsageScope {
  kind: 'task' | 'worker' | 'worker_run';
  id: string;
  /** Project identity for tasks; worker identity for worker runs. */
  project_id?: string;
}

export interface UsageScopeTotal extends UsageScope {
  total_tokens: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  thinking_tokens: number;
  media_cost_usd: number;
  media_receipts: number;
  /** Only observed receipts; this is not proof of complete historical coverage. */
  coverage: 'observed_receipts_only' | '';
  catalog_cost_usd: number;
  provider_cost_usd: number;
  nominal_subscription_cost_usd: number;
  unknown_receipts: number;
  free_receipts: number;
  subscription_receipts: number;
  receipt_count: number;
  history_complete: boolean;
  revision: number;
}

export class SwarmUsageNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  /** Indexed cumulative receipt projection, not current context occupancy.
   * recorded=false means no projection, not proof of free/zero historical usage.
   * Optional date is one indexed UTC day; omitted date is lifetime observed usage.
   * Refresh affected scopes from durable usage.scope.updated events, not polling.
   */
  async scope(scope: UsageScope, date?: string): Promise<{ usage: UsageScopeTotal; recorded: boolean }> {
    if (!scope || !['task', 'worker', 'worker_run'].includes(scope.kind) ||
        typeof scope.id !== 'string' || !scope.id.trim() || scope.id.length > 256 ||
        (scope.kind !== 'worker' && !scope.project_id?.trim()) ||
        (scope.kind === 'worker' && scope.project_id) ||
        (scope.project_id !== undefined && scope.project_id.length > 256)) {
      throw new SwarmValidationError('Valid usage scope identity is required');
    }
    if (date !== undefined && (!/^\d{4}-\d{2}-\d{2}$/.test(date) ||
        !Number.isFinite(Date.parse(`${date}T00:00:00Z`)) ||
        new Date(`${date}T00:00:00Z`).toISOString().slice(0, 10) !== date)) {
      throw new SwarmValidationError('Valid UTC usage date is required');
    }
    const query = new URLSearchParams({ kind: scope.kind, id: scope.id });
    if (scope.project_id) query.set('project_id', scope.project_id);
    if (date) query.set('date', date);
    const result = await this.transport.request<{ usage: UsageScopeTotal; recorded: boolean }>(
      `/v3/usage/scope?${query}`, { method: 'GET' });
    if (!result.data?.usage || typeof result.data.recorded !== 'boolean') {
      throw new SwarmValidationError('Malformed usage scope response');
    }
    return result.data;
  }
}

/** Durable realtime event payload. Hydrate affected cards with scope(); no
 * session subscription or recurring refresh is needed for scope invalidations. */
export interface UsageScopeUpdatedPayload {
  scope_totals: UsageScopeTotal[];
}
