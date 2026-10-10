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
  /** Where this machine forwards its feed (owner only). */
  readonly sinks: SwarmSignalSinksNamespace;

  constructor(private readonly transport: SwarmTransport) {
    this.sinks = new SwarmSignalSinksNamespace(transport);
  }

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

/** Where a machine forwards its signal feed (owner only). */
export interface SignalSink {
  id: string;
  account: string;
  name: string;
  url: string;
  kinds?: string[];
  min_severity?: SignalSeverity;
  heartbeat_seconds: number;
  /** Last signal position the sink accepted. */
  cursor: number;
  created_at: number;
  last_delivered_at?: number;
  last_attempt_at?: number;
  last_error?: string;
  failures?: number;
}

export interface CreateSignalSinkParams {
  name: string;
  /** https URL (http only to this machine's loopback). */
  url: string;
  /** Kind prefixes to forward, e.g. `['agent', 'worker.run.failed']`; empty forwards all. */
  kinds?: string[];
  minSeverity?: SignalSeverity;
  /** Idle heartbeat interval, 30 to 3600 seconds (default 60). */
  heartbeatSeconds?: number;
}

/**
 * Signal sinks: the machine POSTs batches of its feed to each sink, signed
 * with the sink's secret (see `verifySignalDelivery`). Owner only: scoped
 * keys, including admin keys, are refused.
 */
export class SwarmSignalSinksNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  async list(): Promise<SignalSink[]> {
    const res = await this.transport.request<{ ok: boolean; sinks: SignalSink[] }>('/v3/signals/sinks', { method: 'GET' });
    return res.data.sinks ?? [];
  }

  /** Creates a sink. The signing secret is returned only here; store it in the receiver. */
  async create(params: CreateSignalSinkParams): Promise<{ sink: SignalSink; secret: string }> {
    const res = await this.transport.request<{ ok: boolean; sink: SignalSink; secret: string }>('/v3/signals/sinks', {
      method: 'POST',
      body: {
        name: params.name,
        url: params.url,
        kinds: params.kinds,
        min_severity: params.minSeverity,
        heartbeat_seconds: params.heartbeatSeconds,
      },
    });
    return { sink: res.data.sink, secret: res.data.secret };
  }

  async delete(id: string): Promise<void> {
    await this.transport.request(`/v3/signals/sinks/${encodeURIComponent(id)}`, { method: 'DELETE' });
  }
}

/** Body of one signal delivery (version 1). */
export interface SignalDelivery {
  version: 1;
  sink_id: string;
  machine?: string;
  sent_at: number;
  /** True for an idle heartbeat with no signals. */
  heartbeat: boolean;
  signals: Signal[];
  next_after: number;
  latest_seq: number;
  gap: boolean;
}

/**
 * Checks a delivery's `X-Swarm-Signature` for a receiver: HMAC-SHA256 of
 * `${X-Swarm-Timestamp}.${rawBody}` with the sink secret, and a timestamp
 * within `maxSkewMs` of now (rejects replays). Pass the raw request body,
 * not re-serialized JSON. Works with WebCrypto (Node 18+, Workers, browsers).
 */
export async function verifySignalDelivery(
  secret: string,
  timestamp: string | number,
  rawBody: string | Uint8Array,
  signature: string,
  { now = Date.now(), maxSkewMs = 5 * 60 * 1000 }: { now?: number; maxSkewMs?: number } = {},
): Promise<boolean> {
  const ts = Number(timestamp);
  if (!Number.isFinite(ts) || Math.abs(now - ts) > maxSkewMs || !signature.startsWith('v1=')) return false;
  const enc = new TextEncoder();
  const body = typeof rawBody === 'string' ? enc.encode(rawBody) : rawBody;
  const message = new Uint8Array([...enc.encode(`${ts}.`), ...body]);
  const key = await crypto.subtle.importKey('raw', enc.encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const mac = new Uint8Array(await crypto.subtle.sign('HMAC', key, message));
  const expected = 'v1=' + Array.from(mac, (b) => b.toString(16).padStart(2, '0')).join('');
  if (expected.length !== signature.length) return false;
  let diff = 0;
  for (let i = 0; i < expected.length; i++) diff |= expected.charCodeAt(i) ^ signature.charCodeAt(i);
  return diff === 0;
}
