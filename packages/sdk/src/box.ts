import type { SwarmTransport } from './transport.js';

/** `ok`, `attention` (something waits on a person or failed today) or `degraded` (agents unconfined, or spending over the limit). */
export type BoxStatus = 'ok' | 'attention' | 'degraded';

/** One machine's state for a monitor: ids, names and counts only. */
export interface BoxSummary {
  generated_at: number;
  status: BoxStatus;
  /** Why the status is not `ok`. */
  reasons: string[];
  machine: {
    name?: string;
    uptime_ms: number;
    version?: string;
    commit?: string;
    /** The installer's update state (install.sh update), when it has one. */
    update?: {
      auto?: 'on' | 'off';
      channel?: string;
      /** current, waiting, waiting_review, blocked, building, waiting_idle, installed, rolled_back or failed. */
      state?: string;
      reason?: string;
      running?: string;
      candidate?: string;
      at?: number;
    };
    /** The update drain (request_update / the installer): new runs held while running agent runs finish. */
    drain?: {
      draining: boolean;
      since: string;
      deadline: string;
      since_ms: number;
      deadline_ms: number;
      timed_out: boolean;
      /** How many agent runs the drain waits on, across the machine. */
      blocking_runs: number;
      /** The reader's own account's blocking runs (at most a few). */
      blocking: { session_id: string; run_id: string }[];
      /** Blocking runs not listed (other accounts', or past the list). */
      blocking_other: number;
      ready: boolean;
      /** Someone asked for this update (request_update). */
      requested: boolean;
      /** At the deadline, restart over runs still running. */
      force: boolean;
      /** The deadline passed with force: the restart interrupts the blocking runs. */
      forced: boolean;
      /** `expired` (refused at the deadline) or `cancelled`, once ended. */
      end_reason?: string;
      /** What the drain waits on or why it ended, in one line. */
      reason: string;
    };
  };
  /** What a restart of Swarm would interrupt now, across the machine. */
  busy: {
    busy: boolean;
    sessions: number;
    worker_runs: number;
    environment_builds: number;
    /** Workflow run step scripts running now (a restart would kill them). */
    workflow_steps?: number;
  };
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
