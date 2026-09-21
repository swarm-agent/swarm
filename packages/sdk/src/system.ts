import type { SwarmTransport } from './transport.js';
import type { SystemHealth } from './types.js';

export class SwarmSystemNamespace {
  private transport: SwarmTransport;

  constructor(transport: SwarmTransport) {
    this.transport = transport;
  }

  /**
   * Performs a health check against the daemon (/healthz, /readyz, or /health).
   */
  async health(): Promise<SystemHealth> {
    try {
      const res = await this.transport.request<any>('/healthz', {
        method: 'GET',
      });
      if (res.status === 200) {
        if (res.data && typeof res.data === 'object' && res.data.ok !== undefined) {
          return {
            ok: Boolean(res.data.ok),
            status: 'healthy',
            uptime_seconds: res.data.uptime_ms ? Math.floor(res.data.uptime_ms / 1000) : undefined,
          };
        }
        return { ok: true, status: 'healthy' };
      }
    } catch {}

    try {
      const res = await this.transport.request<any>('/readyz', {
        method: 'GET',
      });
      if (res.status === 200) {
        return { ok: true, status: 'ready' };
      }
    } catch {}

    const res = await this.transport.request<any>('/health', {
      method: 'GET',
    });
    return {
      ok: res.status === 200,
      status: res.status === 200 ? 'healthy' : 'unhealthy',
    };
  }

  /**
   * Retrieves operational status from the daemon.
   */
  async status(): Promise<Record<string, unknown>> {
    const res = await this.transport.request<Record<string, unknown>>('/v1/status', {
      method: 'GET',
    });
    return res.data;
  }
}
