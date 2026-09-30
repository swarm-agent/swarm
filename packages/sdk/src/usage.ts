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
   * Refresh affected scopes from durable run.usage.updated events, not polling.
   */
  async scope(scope: UsageScope): Promise<{ usage: UsageScopeTotal; recorded: boolean }> {
    if (!scope || !['task', 'worker', 'worker_run'].includes(scope.kind) ||
        typeof scope.id !== 'string' || !scope.id.trim() || scope.id.length > 256 ||
        (scope.kind !== 'worker' && !scope.project_id?.trim()) ||
        (scope.kind === 'worker' && scope.project_id) ||
        (scope.project_id !== undefined && scope.project_id.length > 256)) {
      throw new SwarmValidationError('Valid usage scope identity is required');
    }
    const query = new URLSearchParams({ kind: scope.kind, id: scope.id });
    if (scope.project_id) query.set('project_id', scope.project_id);
    const result = await this.transport.request<{ usage: UsageScopeTotal; recorded: boolean }>(
      `/v3/usage/scope?${query}`, { method: 'GET' });
    if (!result.data?.usage || typeof result.data.recorded !== 'boolean') {
      throw new SwarmValidationError('Malformed usage scope response');
    }
    return result.data;
  }
}
