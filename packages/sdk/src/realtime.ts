import { SwarmApiError } from './errors.js';
import type { SwarmTransport } from './transport.js';

export interface V3Message {
  id: string; session_id: string; global_seq: number; role: string; content: string; created_at: number;
  metadata?: Record<string, unknown>; [key: string]: unknown;
}
export interface V3Event {
  id: string; session_id: string; seq: number; event_type: string; payload: Record<string, unknown>;
  ts_unix_ms: number; causation_id?: string; correlation_id?: string; epoch_id?: string;
}
export interface V3Projection { session_id: string; last_event_seq: number; projection_high_watermark_seq: number; updated_at: number }
export interface V3Subscription { session_id: string; subscription_id: string; endpoint_cursor?: string }
export interface V3Resume {
  protocol: 'v3.realtime'; protocol_version: 1; kind: 'resume'; endpoint_cursor: string;
  subscriptions: V3Subscription[]; capabilities?: string[];
}
export interface V3LivePatch {
  session_id: string; run_id: string; stream_id: string; stream_kind: string; operation: string;
  step: number; step_id: string; live_seq_start: number; live_seq_end: number;
  offset_start: number; offset_end: number; text: string; recorded_at: number;
}
export interface V3Frame {
  protocol: 'v3.realtime'; protocol_version: 1; kind: string; session_id?: string;
  subscription_id?: string; endpoint_cursor?: string; capabilities?: string[];
  event?: V3Event; projection?: V3Projection; live?: V3LivePatch;
  error?: string; error_code?: string; bootstrap_required?: boolean;
  [key: string]: unknown;
}
export interface V3SyncRequest {
  surface?: string; session_ids: string[]; selector?: { kind: 'session_ids'; session_ids: string[] };
  history?: { mode?: string; max_messages_per_session?: number; max_events_per_session?: number; include_events?: boolean; manifest_policy?: string };
  resources?: { messages?: boolean; events?: boolean; run_intents?: boolean; current_run_state?: boolean; session_view?: boolean; permission_summaries?: boolean; active_plan?: boolean; plan_revisions?: boolean };
  include_active?: boolean;
}
export interface V3ReplayResponse {
  ok: boolean; endpoint_cursor: string; has_more: boolean;
  events: { session_id: string; event_type: string; event: V3Event; projection: V3Projection; [key: string]: unknown }[];
  selector: { kind?: string; session_ids?: string[]; [key: string]: unknown };
  replay_instructions: Record<string, unknown>;
}
export interface V3Permission {
  id: string; session_id: string; run_id: string; call_id: string; tool_name: string;
  tool_arguments: string; requirement: string; mode: string; status: string; decision: string;
  reason: string; created_at: number; updated_at: number; resolved_at: number;
  tool_call_arguments?: string; approved_arguments?: string; proposal_revision?: number;
}
export interface V3RunState {
  session_id: string; run_id: string; active: boolean; status: string; blocked_reason?: string;
  created_at: number; updated_at: number; event_seq: number; started_at?: number; completed_at?: number;
  epoch_id?: string; plan_id?: string; checkpoint_id?: string; attempt_id?: string;
}
export interface V3SessionView {
  pending_permissions?: V3Permission[]; current_run_state?: V3RunState;
  active_plan?: Record<string, unknown>; [key: string]: unknown;
}
/**
 * A resource the daemon left out of (or truncated in) a sync snapshot. With
 * `history.manifest_policy` 'manifest' or 'omit' the recent window is still returned and
 * `next_cursor` points at the older history that was left out.
 */
export interface V3SyncOmission {
  session_id?: string; resource?: 'messages' | 'events' | string; reason?: 'requires_manifest' | string;
  next_cursor?: string; manifest_ref?: string; [key: string]: unknown;
}
export interface V3SyncSnapshot {
  ok: boolean; rev: number; snapshot_endpoint_cursor: string;
  sessions_by_id: Record<string, Record<string, unknown>>;
  projections_by_session: Record<string, V3Projection>;
  messages_by_session: Record<string, V3Message[]>;
  events_by_session: Record<string, V3Event[]>;
  current_run_state_by_session?: Record<string, V3RunState>;
  session_views_by_id?: Record<string, V3SessionView>;
  realtime?: { stream_path: string; resume: V3Resume };
  omissions: V3SyncOmission[];
  pagination: Record<string, unknown>; [key: string]: unknown;
}
/** Minimal WebSocket contract, also usable by authenticated same-origin browser proxies. */
export interface RealtimeSocket {
  send(data: string): void; close(): void;
  addEventListener(type: 'message', listener: (event: { data: unknown }) => void): void;
  addEventListener(type: 'close' | 'error', listener: () => void): void;
}
export type RealtimeSocketFactory = (path: string) => Promise<RealtimeSocket>;
export interface SessionWatchState {
  sessionId: string; snapshot: V3SyncSnapshot; messages: V3Message[];
  /** Transient full text by run/stream. Replace on every update, never append to durable messages. */
  live: ReadonlyArray<{ runId: string; streamId: string; text: string }>;
  status: 'syncing' | 'live' | 'reconnecting';
  /**
   * True when older history did not fit the hydrate window (for example after a very large
   * tool result) and was left out. `messages`/events still hold the most recent window; the
   * conversation stays usable. `historyOmissions` names each resource and its `next_cursor`.
   */
  historyOmitted?: boolean;
  historyOmissions?: V3SyncOmission[];
}
export interface SessionWatchOptions {
  signal?: AbortSignal; surface?: string; socketFactory?: RealtimeSocketFactory;
  onChange(state: SessionWatchState): void; onError?(error: Error): void;
  maxReconnects?: number;
}
export interface SessionWatch { ready: Promise<void>; done: Promise<void>; dispose(): void }

const LIVE_MAX_STREAM_BYTES = 4 * 1024 * 1024;
const LIVE_MAX_STREAMS = 64;
const LIVE_MAX_COMMITTED = 4096;
const utf8Encoder = new TextEncoder();
const utf8Decoder = new TextDecoder('utf-8', { fatal: true });
const DURABLE_TEXT_DELTA_EVENTS = new Set(['session.assistant.delta', 'session.message.delta']);
interface LiveStream { runId: string; streamId: string; text: string; offset: number; held?: { start: number; bytes: Uint8Array } }

/**
 * Transient assistant text assembled from live patches and durable delta checkpoints.
 * UTF-8 byte offsets (not JavaScript string length) are the only placement authority:
 * duplicate and overlapping ranges are trimmed, and a range beyond the contiguous prefix
 * (joining or reconnecting mid-turn, while durable checkpoints lag live output) is held,
 * bounded, until a durable checkpoint fills the gap. Text is never dropped, repeated or
 * shown with a hole. A committed message retires its stream.
 */
export class V3LiveText {
  private streams = new Map<string, LiveStream>();
  private committed = new Set<string>();
  reset(): void { this.streams.clear(); this.committed.clear(); }
  reconcile(messages: V3Message[]): void {
    for (const m of messages) {
      if (m.role !== 'assistant') continue;
      this.commit(m.metadata?.run_id ?? m.metadata?.runId, m.metadata?.stream_id);
    }
  }
  durable(events: V3Event[]): void {
    for (const event of events) {
      const p = event.payload ?? {};
      if (event.event_type === 'session.assistant.completed') {
        const message = p.message as V3Message | undefined;
        if (message?.role === 'assistant') this.reconcile([message]);
        this.commit(p.run_id ?? message?.metadata?.run_id, p.stream_id ?? message?.metadata?.stream_id);
        continue;
      }
      if (!DURABLE_TEXT_DELTA_EVENTS.has(event.event_type)) continue;
      const text = p.delta ?? p.text_delta ?? p.content_delta;
      if (typeof p.run_id !== 'string' || typeof p.stream_id !== 'string' || typeof p.offset_start !== 'number' ||
          typeof p.offset_end !== 'number' || typeof text !== 'string') continue;
      this.append(p.run_id, p.stream_id, p.offset_start, p.offset_end, text);
    }
  }
  /** Returns false only for a malformed patch; gaps and duplicates are handled here. */
  accept(p: V3LivePatch): boolean {
    if (p.stream_kind !== 'assistant_text') return true;
    if (p.operation !== 'append' || typeof p.text !== 'string') return false;
    return this.append(p.run_id, p.stream_id, p.offset_start, p.offset_end, p.text);
  }
  values(): SessionWatchState['live'] {
    return [...this.streams.values()].filter(s => s.text).map(({ runId, streamId, text }) => ({ runId, streamId, text }));
  }
  private commit(run: unknown, stream: unknown): void {
    if (typeof run !== 'string' || typeof stream !== 'string') return;
    const key = JSON.stringify([run, stream]);
    if (!this.committed.has(key) && this.committed.size >= LIVE_MAX_COMMITTED) throw new Error('Live stream history limit reached; reconnect required');
    this.streams.delete(key); this.committed.add(key);
  }
  private append(runId: string, streamId: string, start: number, end: number, text: string): boolean {
    const key = JSON.stringify([runId, streamId]);
    if (this.committed.has(key)) return true;
    const bytes = utf8Encoder.encode(text);
    if (!Number.isSafeInteger(start) || start < 0 || end !== start + bytes.length || end > LIVE_MAX_STREAM_BYTES) return false;
    let stream = this.streams.get(key);
    if (!stream) {
      if (this.streams.size >= LIVE_MAX_STREAMS) return false;
      stream = { runId, streamId, text: '', offset: 0 };
      this.streams.set(key, stream);
    }
    if (end <= stream.offset) return true;
    if (start > stream.offset) { holdLiveRange(stream, start, bytes); return true; }
    if (!extendLiveStream(stream, bytes.subarray(stream.offset - start))) return false;
    const held = stream.held;
    if (held && held.start <= stream.offset) {
      stream.held = undefined;
      if (held.start + held.bytes.length > stream.offset && !extendLiveStream(stream, held.bytes.subarray(stream.offset - held.start))) return false;
    }
    return true;
  }
}
function extendLiveStream(stream: LiveStream, suffix: Uint8Array): boolean {
  try { stream.text += utf8Decoder.decode(suffix); } catch { return false; }
  stream.offset += suffix.length;
  return true;
}
/** Keeps one contiguous range ahead of the prefix; adjoining ranges merge, a later disjoint one replaces it. */
function holdLiveRange(stream: LiveStream, start: number, bytes: Uint8Array): void {
  const held = stream.held;
  const end = start + bytes.length;
  if (held && start <= held.start + held.bytes.length && end >= held.start) {
    const from = Math.min(start, held.start);
    const to = Math.max(end, held.start + held.bytes.length);
    const merged = new Uint8Array(to - from);
    merged.set(held.bytes, held.start - from);
    merged.set(bytes, start - from);
    stream.held = { start: from, bytes: merged };
    return;
  }
  if (!held || end > held.start + held.bytes.length) stream.held = { start, bytes: bytes.slice() };
}

export class SwarmRealtimeNamespace {
  constructor(private readonly transport: SwarmTransport) {}
  async bootstrap(body: V3SyncRequest, signal?: AbortSignal): Promise<V3SyncSnapshot> {
    return (await this.transport.request<V3SyncSnapshot>('/v3/sync/bootstrap', { method: 'POST', body, signal })).data;
  }
  async hydrate(body: V3SyncRequest, signal?: AbortSignal): Promise<V3SyncSnapshot> {
    return (await this.transport.request<V3SyncSnapshot>('/v3/sync/hydrate', { method: 'POST', body, signal })).data;
  }
  /**
   * Hydrates a watch window. The daemon's default manifest policy ('error') refuses the whole
   * snapshot when the recent window cannot hold all history (e.g. one ~280 KB tool result
   * exhausts the 256 KB message budget). Both 'omit' and 'manifest' still return the same
   * newest window plus an omission with `next_cursor`; 'manifest' also returns a descriptor
   * of what was included, so it loses the least. The policy then sticks for this watch.
   */
  private async hydrateWindow(body: V3SyncRequest, signal?: AbortSignal): Promise<V3SyncSnapshot> {
    try {
      return await this.hydrate(body, signal);
    } catch (error) {
      if (body.history?.manifest_policy || !isRequiresManifestError(error)) throw error;
      body.history = { ...body.history, manifest_policy: 'manifest' };
      return this.hydrate(body, signal);
    }
  }
  /** Bounded durable replay. Keep the identical selector/resources/surface for every cursor. */
  async replay(body: V3SyncRequest & { endpoint_cursor: string; limit?: number }, signal?: AbortSignal): Promise<V3ReplayResponse> {
    return (await this.transport.request<V3ReplayResponse>('/v3/sync/stream', { method: 'POST', body, signal })).data;
  }
  /** Node uses ws over the private Unix socket or header-authenticated HTTP upgrade. */
  async socket(path: string): Promise<RealtimeSocket> {
    const config = this.transport.getConfig();
    if (!path.startsWith('/v3/realtime/stream?')) throw new Error('Invalid realtime path');
    if (typeof process === 'undefined' || !process.versions?.node) {
      if (config.token || config.socketPath || Object.keys(config.defaultHeaders).length) throw new Error('Browser realtime requires a same-origin authenticated proxy; do not expose admin tokens');
      const url = new URL(config.baseUrl);
      if (typeof location === 'undefined' || url.origin !== location.origin) throw new Error('Browser realtime requires same-origin proxy');
      return new WebSocket(`${url.origin.replace(/^http/, 'ws')}${path}`);
    }
    const moduleName = 'ws';
    const module = await import(moduleName) as { default: new (url: string, options: object) => RealtimeSocket };
    const headers = { ...config.defaultHeaders, ...(config.token ? { Authorization: `Bearer ${config.token}` } : {}) };
    const url = config.socketPath ? `ws+unix://${config.socketPath}:${path}` : `${config.baseUrl.replace(/^http/, 'ws')}${path}`;
    const socket = new module.default(url, { headers, followRedirects: false, handshakeTimeout: config.timeoutMs, maxPayload: 8 * 1024 * 1024 });
    // ws emits an error when a connecting socket is disposed before upgrade.
    socket.addEventListener('error', () => {});
    return socket;
  }
  watchSession(sessionId: string, options: SessionWatchOptions): SessionWatch {
    if (!sessionId.trim()) throw new Error('sessionId is required');
    const controller = new AbortController();
    const signal = controller.signal;
    const abort = () => controller.abort();
    options.signal?.addEventListener('abort', abort, { once: true });
    if (options.signal?.aborted) abort();
    let resolveReady!: () => void;
    let rejectReady!: (error: Error) => void;
    const ready = new Promise<void>((resolve, reject) => { resolveReady = resolve; rejectReady = reject; });
    // A caller may await only done; prevent an unobserved ready rejection.
    void ready.catch(() => {});
    const done = this.runWatch(sessionId, options, signal, resolveReady).catch((error: unknown) => {
      const err = error instanceof Error ? error : new Error(String(error));
      rejectReady(err);
      if (!signal.aborted) { options.onError?.(err); throw err; }
    }).finally(() => { rejectReady(new Error('Watch disposed')); options.signal?.removeEventListener('abort', abort); });
    void done.catch(() => {});
    return { ready, done, dispose: abort };
  }
  private async runWatch(id: string, options: SessionWatchOptions, signal: AbortSignal, ready: () => void): Promise<void> {
    const surface = options.surface ?? 'desktop';
    const body: V3SyncRequest = { surface, session_ids: [id], selector: { kind: 'session_ids', session_ids: [id] },
      history: { mode: 'tail', max_messages_per_session: 200, max_events_per_session: 200, include_events: true },
      resources: { messages: true, events: true, current_run_state: true, session_view: true } };
    // The budget bounds consecutive failures only; every replayed connection restores it.
    let attempts = 0;
    let latest: SessionWatchState | undefined;
    const onChange = (state: SessionWatchState) => { latest = state; options.onChange(state); };
    while (!signal.aborted) {
      // Every reconnect hydrates fresh durable authority before replay; no timer polling.
      const snapshot = await this.hydrateWindow(body, signal);
      if (signal.aborted) return;
      if (!snapshot.sessions_by_id[id] || !snapshot.realtime?.resume.endpoint_cursor) throw new Error('Session omitted or realtime bootstrap unavailable');
      try {
        await this.connection(id, body, snapshot, latest?.snapshot.events_by_session[id] ?? [], { ...options, onChange }, signal, ready, () => { attempts = 0; });
        return;
      } catch (error) {
        if (signal.aborted) return;
        if (error instanceof FatalRealtimeError || attempts >= (options.maxReconnects ?? 6)) throw error;
        // Keep the last rendered conversation (including partial text) while reconnecting.
        onChange({ ...(latest ?? { sessionId: id, snapshot, messages: snapshot.messages_by_session[id] ?? [], live: [], ...historyOmissionState(snapshot, id) }), status: 'reconnecting' });
        await delay(Math.min(250 * 2 ** attempts++, 8000), signal);
      }
    }
  }
  private async connection(id: string, body: V3SyncRequest, initial: V3SyncSnapshot, seenEvents: V3Event[], options: SessionWatchOptions, signal: AbortSignal, ready: () => void, onReplayed: () => void): Promise<void> {
    const socket = await (options.socketFactory ?? ((path) => this.socket(path)))(`/v3/realtime/stream?${new URLSearchParams({ surface: body.surface! })}`);
    if (signal.aborted) { socket.close(); return; }
    return new Promise<void>((resolve, reject) => {
      const requests = new AbortController();
      let snapshot = initial; let ended = false; let replayed = false; let hello = false; let liveNegotiated = false;
      let refreshing = false; let dirty = false; let readyPending = false; let lastSeq = snapshot.projections_by_session[id]?.last_event_seq ?? 0;
      // Durable events delivered on the socket are merged into the hydrated tail, so a
      // consumer sees every event once and immediately, even before the next hydration.
      let events = mergeSessionEvents(seenEvents, snapshot.events_by_session[id] ?? []);
      const live = new V3LiveText(); live.reconcile(snapshot.messages_by_session[id] ?? []);
      live.durable(events);
      const subscription = `sdk:session:${id}`;
      const timer = setTimeout(() => finish(new Error('Realtime handshake timed out')), 30_000);
      const emit = () => {
        if (ended) return;
        const view = { ...snapshot, events_by_session: { ...snapshot.events_by_session, [id]: events } };
        options.onChange({ sessionId: id, snapshot: view, messages: snapshot.messages_by_session[id] ?? [], live: live.values(), status: replayed ? 'live' : 'syncing', ...historyOmissionState(snapshot, id) });
      };
      const finish = (error?: Error) => {
        if (ended) return; ended = true; requests.abort(); clearTimeout(timer); signal.removeEventListener('abort', cancel); socket.close();
        if (error) reject(error); else resolve();
      };
      const cancel = () => finish();
      const refresh = async () => {
        dirty = true;
        if (refreshing) return;
        refreshing = true;
        try {
          while (dirty && !ended) {
            dirty = false;
            const next = await this.hydrateWindow(body, requests.signal);
            if (ended) return;
            if (!next.sessions_by_id[id]) throw new FatalRealtimeError('Session no longer available');
            snapshot = next;
            lastSeq = Math.max(lastSeq, next.projections_by_session[id]?.last_event_seq ?? 0);
            events = mergeSessionEvents(events, next.events_by_session[id] ?? []);
            live.reconcile(next.messages_by_session[id] ?? []);
            live.durable(next.events_by_session[id] ?? []);
            emit();
          }
          if (readyPending && !ended) { readyPending = false; ready(); }
        } catch (e) { finish(e instanceof Error ? e : new Error(String(e))); }
        finally { refreshing = false; }
      };
      signal.addEventListener('abort', cancel, { once: true });
      socket.addEventListener('close', () => finish(new Error('Realtime disconnected')));
      socket.addEventListener('error', () => finish(new Error('Realtime transport failed')));
      socket.addEventListener('message', ({ data }) => {
        if (ended) return;
        try {
          const frame = JSON.parse(String(data)) as V3Frame;
          if (frame.protocol !== 'v3.realtime' || frame.protocol_version !== 1) throw new FatalRealtimeError('Unsupported realtime protocol');
          if (frame.kind === 'auth.denied') throw new FatalRealtimeError(frame.error ?? 'Realtime authentication denied');
          if (frame.kind === 'cursor.error' || frame.kind === 'slow_consumer.reconnect_required') throw new Error(frame.error_code ?? frame.kind);
          if (frame.kind === 'hello') {
            if (hello || !frame.endpoint_cursor) throw new FatalRealtimeError('Invalid realtime hello');
            hello = true;
            liveNegotiated = frame.capabilities?.includes('live_patch_v1') ?? false;
            // Use the server-provided snapshot handoff cursor, never the later hello head.
            const resume: V3Resume = { protocol: 'v3.realtime', protocol_version: 1, kind: 'resume',
              endpoint_cursor: initial.realtime!.resume.endpoint_cursor,
              subscriptions: [{ session_id: id, subscription_id: subscription, endpoint_cursor: initial.realtime!.resume.endpoint_cursor }],
              capabilities: liveNegotiated ? ['live_patch_v1'] : [] };
            socket.send(JSON.stringify(resume)); emit(); return;
          }
          if (!hello) throw new FatalRealtimeError('Realtime frame before hello');
          if (frame.session_id !== id) return;
          if (frame.subscription_id && frame.subscription_id !== subscription) return;
          if (frame.kind === 'replay.complete') { replayed = true; onReplayed(); clearTimeout(timer); readyPending = true; void refresh(); }
          else if (frame.kind === 'live.patch' && frame.live?.session_id === id) {
            if (!liveNegotiated || !replayed) return;
            if (!live.accept(frame.live)) { finish(new Error('Malformed live patch; durable rehydration required')); } else emit();
          } else if (frame.kind === 'event' && frame.event?.session_id === id) {
            if (frame.event.seq <= lastSeq) return;
            lastSeq = frame.event.seq;
            events = mergeSessionEvents(events, [frame.event]);
            live.durable([frame.event]);
            emit();
            // Streaming checkpoints change no hydrated authority (messages, run state,
            // permissions); re-hydrating on each one would only add load.
            if (!String(frame.event.event_type).endsWith('.delta')) void refresh();
          } else if (frame.kind === 'projection.high_watermark') { void refresh(); }
        } catch (e) { finish(e instanceof Error ? e : new Error(String(e))); }
      });
    });
  }
}
const MAX_WATCH_EVENTS = 1000;
/** Union by durable sequence, ascending, bounded to the newest events. */
function mergeSessionEvents(current: V3Event[], incoming: V3Event[]): V3Event[] {
  if (!incoming.length) return current;
  const bySeq = new Map<number, V3Event>();
  for (const event of current) bySeq.set(event.seq, event);
  for (const event of incoming) if (event && Number.isSafeInteger(event.seq) && !bySeq.has(event.seq)) bySeq.set(event.seq, event);
  const merged = [...bySeq.values()].sort((a, b) => a.seq - b.seq);
  return merged.length > MAX_WATCH_EVENTS ? merged.slice(merged.length - MAX_WATCH_EVENTS) : merged;
}
class FatalRealtimeError extends Error {}
function delay(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const finish = () => { clearTimeout(timer); signal.removeEventListener('abort', finish); resolve(); };
    const timer = setTimeout(finish, ms);
    signal.addEventListener('abort', finish, { once: true });
    if (signal.aborted) finish();
  });
}
function isRequiresManifestError(error: unknown): boolean {
  return error instanceof SwarmApiError && error.status === 400 && /\brequires_manifest\b/.test(error.message);
}
function historyOmissionState(snapshot: V3SyncSnapshot, id: string): Pick<SessionWatchState, 'historyOmitted' | 'historyOmissions'> {
  const historyOmissions = (Array.isArray(snapshot.omissions) ? snapshot.omissions : [])
    .filter((o) => o && o.session_id === id && (o.resource === 'messages' || o.resource === 'events'));
  return { historyOmitted: historyOmissions.length > 0, historyOmissions };
}
