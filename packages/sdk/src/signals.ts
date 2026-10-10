import type { SwarmTransport } from './transport.js';

export type SignalSeverity = 'info' | 'warning' | 'critical';

/**
 * One entry in a machine's signal feed: a high-level event such as an agent
 * waiting on a person, a failed run, a refused key or an event an outside
 * source reported. Signals carry ids and a one-line summary, never transcripts,
 * command arguments or secret values.
 */
export interface Signal {
  /** Position in this machine's feed; pass the last one back as `after`. */
  seq: number;
  /** Unique id, stable across forwarding (use it to drop duplicates). */
  id: string;
  /** Unix milliseconds. */
  at: number;
  /** Dotted kind, e.g. `agent.blocked`, `run.failed`, `external.falco`. */
  kind: string;
  severity: SignalSeverity;
  /** `swarmd` for Swarm's own signals, `external:<name>` for reported ones. */
  source: string;
  /** Empty for machine-wide signals. */
  account?: string;
  summary: string;
  /** Signals with the same key describe the same ongoing condition. */
  dedup_key?: string;
  refs?: Record<string, string>;
  attrs?: Record<string, string>;
}

export interface SignalPage {
  signals: Signal[];
  /** The last position examined; pass it as `after` to continue. */
  next_after: number;
  oldest_seq: number;
  latest_seq: number;
  /**
   * True when signals after your cursor were already dropped (the feed keeps
   * the newest entries only) or your cursor is ahead of the feed. Resync
   * from `next_after` and treat the missed range as unknown.
   */
  gap: boolean;
}

export interface ListSignalsParams {
  /** Return signals numbered after this position (default 0: from the oldest kept). */
  after?: number;
  /** At most this many signals (default 100, maximum 500). */
  limit?: number;
  /** Kind prefixes, e.g. `['agent', 'run.failed']`. */
  kinds?: string[];
  /** Only signals at least this severe. */
  minSeverity?: SignalSeverity;
}

export interface ReportSignalParams {
  /** Must start with `external.`, e.g. `external.falco`. */
  kind: string;
  /** One line; longer text is cut to 300 characters. */
  summary: string;
  severity?: SignalSeverity;
  /** Lowercase name of the reporting source, e.g. `falco`. */
  source?: string;
  dedup_key?: string;
  refs?: Record<string, string>;
  attrs?: Record<string, string>;
}

/**
 * The machine signal feed. Reading needs a key with `signals:read`; reporting
 * an outside event needs `signals:write`. Readers see their own account's
 * signals and machine-wide ones.
 */
export class SwarmSignalsNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  /** Lists signals after a cursor, oldest first. */
  async list(params: ListSignalsParams = {}): Promise<SignalPage> {
    const q = new URLSearchParams();
    if (typeof params.after === 'number') q.set('after', String(params.after));
    if (typeof params.limit === 'number') q.set('limit', String(params.limit));
    if (params.kinds?.length) q.set('kind', params.kinds.join(','));
    if (params.minSeverity) q.set('min_severity', params.minSeverity);
    const path = q.toString() ? `/v3/signals?${q.toString()}` : '/v3/signals';
    const res = await this.transport.request<SignalPage & { ok: boolean }>(path, { method: 'GET' });
    const { signals, next_after, oldest_seq, latest_seq, gap } = res.data;
    return { signals: signals ?? [], next_after, oldest_seq, latest_seq, gap };
  }

  /**
   * Reports an outside event (a host security monitor, a backup script) into
   * the feed. Swarm records the source as `external:<source>` and, for a
   * scoped key, which key reported it.
   */
  async report(params: ReportSignalParams): Promise<Signal> {
    const res = await this.transport.request<{ ok: boolean; signal: Signal }>('/v3/signals', {
      method: 'POST',
      body: params,
    });
    return res.data.signal;
  }
}
