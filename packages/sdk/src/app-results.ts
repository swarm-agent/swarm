import type { SwarmTransport } from './transport.js';
import { SwarmRealtimeNamespace } from './realtime.js';
import type { RealtimeSocketFactory, SessionWatch, V3SyncSnapshot, V3Frame } from './realtime.js';
import type { SwarmAppsNamespace, ApplicationAgent } from './apps.js';
import type { ProjectTaskRecord, WorkerRecord, WorkerRunRecord } from './types.js';

export interface ApplicationResults {
  agent: ApplicationAgent;
  tasks: ProjectTaskRecord[];
  workers: Array<{ worker: WorkerRecord; runs: WorkerRunRecord[] }>;
}
export interface ApplicationResultsOptions {
  signal?: AbortSignal;
  socketFactory?: RealtimeSocketFactory;
  onChange(results: ApplicationResults): void;
  onError?(error: Error): void;
  maxReconnects?: number;
}

/** Resource notifications invalidate authorized snapshots, never supply app data.
 * Each connection starts with a durable bootstrap cursor BEFORE reading resources.
 * Reconnect rehydrates everything; cursors are never parsed or shared across scopes.
 */
export function watchApplicationResults(apps: SwarmAppsNamespace, transport: SwarmTransport, id: string, options: ApplicationResultsOptions): SessionWatch {
  const controller = new AbortController(), signal = controller.signal;
  const abort = () => controller.abort();
  options.signal?.addEventListener('abort', abort, { once: true });
  if (options.signal?.aborted) abort();
  let resolveReady!: () => void, rejectReady!: (e: Error) => void;
  const ready = new Promise<void>((resolve, reject) => { resolveReady = resolve; rejectReady = reject; });
  void ready.catch(() => {});
  const realtime = new SwarmRealtimeNamespace(transport);
  const snapshot = async (readSignal: AbortSignal = signal): Promise<ApplicationResults> => {
    const agent = await apps.get(id, readSignal);
    const tasks = agent.project_id ? (await apps.tasks(id, readSignal)).tasks : [];
    const workers: ApplicationResults['workers'] = [];
    // Sequential resource reads bound concurrency even with 100 linked workers.
    for (const workerId of agent.worker_ids ?? []) {
      if (signal.aborted) throw new Error('Watch disposed');
      const { worker } = await apps.worker(id, workerId, readSignal);
      const { runs } = await apps.runs(id, workerId, readSignal);
      workers.push({ worker, runs });
    }
    return { agent, tasks, workers };
  };
  const connect = async () => {
    await apps.get(id, signal); // Reject foreign app ownership before opening any event scope.
    // Principal-scoped global workset is the existing resource event contract.
    // A bounded recent membership is requested; unrelated session payloads are ignored.
    const initial = (await transport.request<V3SyncSnapshot>('/v3/sync/bootstrap', {
      method: 'POST', signal, body: { surface: 'desktop', selector: { kind: 'global', global: true, recent: { limit: 1 } }, history: { mode: 'none' } },
    })).data;
    if (signal.aborted) return;
    if (!initial.realtime?.resume.endpoint_cursor) throw new Error('Resource realtime unavailable');
    let state = await snapshot();
    if (signal.aborted) return;
    const socket = await (options.socketFactory ?? (path => realtime.socket(path)))('/v3/realtime/stream?surface=desktop');
    if (signal.aborted) { socket.close(); return; }
    await new Promise<void>((resolve, reject) => {
      const requests = new AbortController();
      let ended = false, hello = false, confirmed = false, refreshing = false, dirty = false;
      const finish = (error?: Error) => {
        if (ended) return;
        ended = true; requests.abort(); clearTimeout(timer); signal.removeEventListener('abort', cancel); socket.close();
        error ? reject(error) : resolve();
      };
      const cancel = () => finish();
      const timer = setTimeout(() => finish(new Error('Resource handshake timed out')), 30_000);
      const refresh = async () => {
        dirty = true;
        if (refreshing) return;
        refreshing = true;
        try {
          while (dirty && !ended) {
            dirty = false; const next = await snapshot(requests.signal);
            if (ended) return;
            state = next;
            if (confirmed) { options.onChange(state); resolveReady(); }
          }
        } catch (error) { finish(error instanceof Error ? error : new Error('Resource refresh failed')); }
        finally { refreshing = false; }
      };
      signal.addEventListener('abort', cancel, { once: true });
      socket.addEventListener('close', () => finish(new Error('Resource stream disconnected')));
      socket.addEventListener('error', () => finish(new Error('Resource stream failed')));
      socket.addEventListener('message', ({ data }) => {
        if (ended) return;
        try {
          const text = String(data);
          if (text.length > 8 * 1024 * 1024) throw new Error('Resource frame too large');
          const frame = JSON.parse(text) as V3Frame;
          if (frame.protocol !== 'v3.realtime' || frame.protocol_version !== 1) throw new Error('Unsupported resource protocol');
          if (frame.kind === 'auth.denied') throw Object.assign(new Error('Resource authorization denied'), { status: 403 });
          if (['cursor.error', 'slow_consumer.reconnect_required'].includes(frame.kind)) throw new Error('Resource stream requires reauthorization');
          if (frame.kind === 'hello') {
            if (hello) throw new Error('Duplicate hello');
            hello = true;
            socket.send(JSON.stringify({ ...initial.realtime!.resume, subscriptions: [] }));
            return;
          }
          if (!hello) throw new Error('Resource frame before hello');
          // Resource-only resumes have no replay.done acknowledgment. The first
          // resource/watermark/keepalive is a liveness fence, not an atomic snapshot.
          // Re-read after it while coalescing replay invalidations; hello alone is
          // not readiness and an immediate resume denial must never emit data.
          if (!confirmed && ['keepalive', 'endpoint.watermark', 'project.updated', 'worker.updated', 'automation.updated'].includes(frame.kind)) {
            confirmed = true; clearTimeout(timer); void refresh(); return;
          }
          if ((frame.kind === 'project.updated' && frame.project_id === state.agent.project_id) ||
              (['worker.updated', 'automation.updated'].includes(frame.kind) && state.workers.length > 0)) void refresh();
        } catch (error) { finish(error instanceof Error ? error : new Error('Invalid resource frame')); }
      });
    });
  };
  const done = (async () => {
    for (let attempt = 0; !signal.aborted; attempt++) {
      try { await connect(); return; }
      catch (error) {
        if (signal.aborted) return;
        // Authorization errors are terminal, never retried against a different route.
        const status = (error as { status?: number }).status;
        if ([401, 403, 404].includes(status ?? 0) || attempt >= (options.maxReconnects ?? 3)) throw error;
        await new Promise<void>(resolve => {
          const finish = () => { clearTimeout(timer); signal.removeEventListener('abort', finish); resolve(); };
          const timer = setTimeout(finish, Math.min(250 * 2 ** attempt, 4000));
          signal.addEventListener('abort', finish, { once: true });
          if (signal.aborted) finish();
        });
      }
    }
  })().catch((error: unknown) => {
    const err = error instanceof Error ? error : new Error('Result watch failed');
    rejectReady(err); if (!signal.aborted) { options.onError?.(err); throw err; }
  }).finally(() => { rejectReady(new Error('Watch disposed')); options.signal?.removeEventListener('abort', abort); });
  void done.catch(() => {});
  return { ready, done, dispose: abort };
}
