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
  coverage: 'observed_receipts_only' | 'repaired_receipts_incomplete' | 'no_records' | '';
  catalog_cost_usd: number;
  provider_cost_usd: number;
  provider_estimate_cost_usd: number;
  nominal_subscription_cost_usd: number;
  unknown_receipts: number;
  free_receipts: number;
  subscription_receipts: number;
  receipt_count: number;
  history_complete: boolean;
  revision: number;
}

export interface UsageScopeRepairResult {
  scanned: number;
  repaired: number;
  unresolved: number;
  next_cursor?: string;
  history_complete: boolean;
}

export class SwarmUsageNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  async workerBudget(workerId: string): Promise<WorkerBudgetStatus> {
    const status = await this.requestWorkerBudget(workerId) as WorkerBudgetStatus;
    if (!/^\d{4}-\d{2}-\d{2}$/.test(status.date) || !status.usage ||
        typeof status.blocked !== 'boolean' || typeof status.inflight !== 'boolean' ||
        !status.account_policy || !status.account_usage || typeof status.account_inflight !== 'boolean' ||
        typeof status.limitations !== 'string' || !['no_records', 'observed_receipts_only', 'legacy_pricing_incomplete'].includes(status.account_coverage) ||
        ![status.remaining_cost_usd, status.remaining_tokens, status.account_remaining_cost_usd, status.account_remaining_tokens]
          .every(value => value === null || (typeof value === 'number' && Number.isFinite(value) && value >= 0))) {
      throw new SwarmValidationError('Malformed worker budget status');
    }
    return status;
  }

  /** User-only revision-guarded policy. Zero unsets a cap, never usage. In-flight
   * provider work may overshoot; this is not an invoice-hard spending guarantee. */
  async setWorkerBudget(workerId: string, policy: WorkerBudgetUpdate): Promise<WorkerBudgetPolicy> {
    if (!policy || !Number.isSafeInteger(policy.expected_revision) || policy.expected_revision < 0 ||
        !Number.isFinite(policy.daily_cost_limit_usd) || policy.daily_cost_limit_usd < 0 ||
        !Number.isSafeInteger(policy.daily_tokens_limit) || policy.daily_tokens_limit < 0) {
      throw new SwarmValidationError('Valid worker budget policy is required');
    }
    return this.requestWorkerBudget(workerId, policy);
  }

  private async requestWorkerBudget(workerId: string, policy?: WorkerBudgetUpdate): Promise<WorkerBudgetPolicy> {
    if (typeof workerId !== 'string' || !/^[a-zA-Z0-9_.-]{3,128}$/.test(workerId)) {
      throw new SwarmValidationError('Valid worker identity is required');
    }
    const result = await this.transport.request<WorkerBudgetPolicy>(
      `/v3/usage/worker-budget?${new URLSearchParams({ worker_id: workerId })}`,
      policy ? { method: 'PUT', body: policy } : { method: 'GET' });
    if (!result.data || result.data.worker_id !== workerId || !Number.isSafeInteger(result.data.revision)) {
      throw new SwarmValidationError('Malformed worker budget response');
    }
    return result.data;
  }

  /** Explicit bounded maintenance; no account charges are replayed. A finished
   * cursor does not certify unavailable historic lineage or receipt indexes. */
  async repair(cursor = '', limit = 100): Promise<UsageScopeRepairResult> {
    if (typeof cursor !== 'string' || cursor.length > 3072 || !Number.isInteger(limit) || limit < 1 || limit > 100) {
      throw new SwarmValidationError('Valid bounded usage repair request is required');
    }
    const result = await this.transport.request<UsageScopeRepairResult>('/v3/usage/scopes/repair', {
      method: 'POST', body: { cursor, limit },
    });
    if (!result.data || typeof result.data.history_complete !== 'boolean') {
      throw new SwarmValidationError('Malformed usage repair response');
    }
    return result.data;
  }

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

export interface WorkerBudgetUpdate {
  expected_revision: number;
  daily_cost_limit_usd: number;
  daily_tokens_limit: number;
}

export interface WorkerBudgetPolicy {
  account_scope_id: string;
  worker_id: string;
  revision: number;
  daily_cost_limit_usd: number;
  daily_tokens_limit: number;
  updated_at: number;
}

/** Indexed observed receipts, never an invoice-hard guarantee. Null remaining is unset. */
export interface WorkerBudgetStatus extends WorkerBudgetPolicy {
  date: string;
  usage: UsageScopeTotal;
  remaining_cost_usd: number | null;
  remaining_tokens: number | null;
  blocked: boolean;
  blocked_reason?: string;
  inflight: boolean;
  account_policy: { account_scope_id: string; enabled: boolean; daily_cost_limit_usd: number; daily_tokens_limit?: number; updated_at: number };
  account_usage: { account_scope_id: string; date: string; total_cost_usd: number; total_tokens: number; unknown_receipts?: number; pricing_coverage_version?: number; pricing_coverage_incomplete?: boolean };
  account_remaining_cost_usd: number | null;
  account_remaining_tokens: number | null;
  account_inflight: boolean;
  account_coverage: 'no_records' | 'observed_receipts_only' | 'legacy_pricing_incomplete';
  limitations: string;
}
