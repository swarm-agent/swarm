import type { SwarmTransport } from './transport.js';

/** `ok`, `attention` (something waits on a person or failed today) or `degraded` (agents unconfined, or spending over the limit). */
export type BoxStatus = 'ok' | 'attention' | 'degraded';

/** One machine's state for a monitor: ids, names and counts only. */
export interface BoxSummary {
  generated_at: number;
  status: BoxStatus;
  /** Why the status is not `ok`. */
  reasons: string[];
  machine: { name?: string; uptime_ms: number };
  sandbox: { mode: string; active: boolean; runtime?: string; reason?: string };
  secrets_gateway: boolean;
  permissions: { bypass: boolean; bypass_blocked_reason?: string };
  attention: {
    blocked_sessions: number;
    oldest_pending_at?: number;
    sessions: { session_id: string; pending: number; oldest_pending_at: number }[];
  };
  workers: {
    total: number;
    truncated?: boolean;
    active_runs: number;
    today_runs: number;
    today_failed: number;
    /** UTC day the counts cover, YYYY-MM-DD. */
    day?: string;
    failing: { worker_id: string; name: string; today_runs: number; today_failed: number; active_runs: number }[];
  };
  usage: {
    today_cost_usd?: number;
    today_tokens?: number;
    daily_cost_limit_usd?: number;
    limit_enabled?: boolean;
    limit_exceeded?: boolean;
  };
  signals: { latest_seq?: number; oldest_seq?: number };
}

/** Machine-level state. Needs a key with `signals:read`. */
export class SwarmBoxNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  /** Is this machine all right, and does anything need a person? One cheap call. */
  async summary(): Promise<BoxSummary> {
    const res = await this.transport.request<BoxSummary & { ok: boolean }>('/v3/box/summary', { method: 'GET' });
    const { ok: _ok, ...summary } = res.data;
    return summary;
  }
}
