import type { SwarmTransport } from './transport.js';

export interface ActivityEvent {
  seq: number; at: number; kind: string; session_id: string; job_id: string;
  worker_id: string; worker_name: string; project_id: string; state: string; summary: string;
  tool_name?: string; file_path?: string;
}
export interface ActivityGap { kind: 'gap'; from: number; to: number; cursor: string }
export interface ActivityStreamOptions { cursor?: string; project?: string; worker?: string; signal?: AbortSignal }

export class SwarmActivityNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  /** Metadata only. Gap records must be surfaced to users; they are not activity events. */
  async *stream(options: ActivityStreamOptions = {}): AsyncGenerator<ActivityEvent | ActivityGap> {
    let cursor = options.cursor ?? '0';
    while (!options.signal?.aborted) {
      const config = this.transport.getConfig();
      if (config.socketPath) throw new Error('Activity SSE requires an HTTP connection');
      const query = new URLSearchParams({ cursor });
      if (options.project) query.set('project', options.project);
      if (options.worker) query.set('worker', options.worker);
      const headers: Record<string, string> = { ...config.defaultHeaders, Accept: 'text/event-stream' };
      if (config.token && !headers.Authorization && !headers.authorization) headers.Authorization = `Bearer ${config.token}`;
      try {
      const response = await fetch(`${config.baseUrl.replace(/\/+$/, '')}/v3/activity/stream?${query}`, { headers, signal: options.signal, redirect: 'error' });
      if (!response.ok || !response.body) throw new Error(`Activity stream failed: ${response.status}`);
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let pending = '';
      try {
        for (;;) {
          const chunk = await reader.read();
          if (chunk.done) break;
          pending += decoder.decode(chunk.value, { stream: true }).replace(/\r\n/g, '\n');
          if (pending.length > 1024 * 1024) throw new Error('Activity frame too large');
          let end: number;
          while ((end = pending.indexOf('\n\n')) >= 0) {
            const frame = pending.slice(0, end); pending = pending.slice(end + 2);
            const kind = frame.split('\n').find(line => line.startsWith('event:'))?.slice(6).trim();
            const data = frame.split('\n').filter(line => line.startsWith('data:')).map(line => line.slice(5).trimStart()).join('\n');
            if (!data) continue;
            const value = JSON.parse(data);
            if (kind === 'cursor') cursor = value.cursor;
            else if (kind === 'gap') { cursor = value.cursor; yield { ...value, kind: 'gap' }; }
            else if (kind === 'activity') { cursor = String(value.seq); yield value as ActivityEvent; }
          }
        }
      } finally { try { await reader.cancel(); } catch { /* The disconnected transport is already closed. */ } reader.releaseLock(); }
      } catch (error) {
        if (options.signal?.aborted) return;
        // fetch and stream transport failures are TypeErrors; malformed frames
        // and HTTP authorization failures are not retried.
        if (!(error instanceof TypeError)) throw error;
      }
      // Bound reconnect frequency; this is reconnection, not polling for activity.
      if (!options.signal?.aborted) await new Promise<void>(resolve => {
        const finish = () => { clearTimeout(timer); options.signal?.removeEventListener('abort', finish); resolve(); };
        const timer = setTimeout(finish, 1000);
        options.signal?.addEventListener('abort', finish, { once: true });
      });
    }
  }
}
