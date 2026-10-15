import type { SwarmTransport } from './transport.js';

export interface BoxLimits {
  revision: number;
  worker_job_concurrency: number;
  job_tokens: number;
  job_cost_usd: number;
  delegation_waves: number;
  active_children: number;
  swarm_children: number;
}

export class SwarmLimitsNamespace {
  constructor(private readonly transport: SwarmTransport) {}
  async get(): Promise<BoxLimits> {
    return (await this.transport.request<BoxLimits>('/v3/limits', { method: 'GET' })).data;
  }
  async set(expected_revision: number, limits: Omit<BoxLimits, 'revision'>): Promise<BoxLimits> {
    return (await this.transport.request<BoxLimits>('/v3/limits', { method: 'PUT', body: { expected_revision, limits } })).data;
  }
}
