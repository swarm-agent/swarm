import type { SwarmTransport } from './transport.js';
import type { HostChange, HostChangeList, HostChangeOp, HostChangeStatus } from './types.js';

/**
 * Owner-approved changes to the machine Swarm runs on. A headless Swarm runs
 * as its own user and sees only the folders it was given: `request` records a
 * `mount-workspace` (or `unmount-workspace`) request for one folder (scoped
 * keys need `host:request`, minted with `swarmctl setup sdk-token
 * --host-request`); `list` follows it (`host:request` or `swarm:read`); only
 * the machine owner's credential can `approve` or `deny` (every scoped key,
 * including admin keys, is refused). Approval runs the installer-placed helper
 * and restarts Swarm; add the folder again afterwards.
 */
export class SwarmHostNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  /**
   * Records a request. An identical pending request is returned instead of
   * duplicated. Rejects with `SwarmConflictError` code `host_changes_unsupported`
   * when this machine has no host-change helper.
   */
  async request(body: { op: HostChangeOp; path: string; reason: string }): Promise<HostChange> {
    return (await this.transport.request<{ ok: boolean; change: HostChange }>('/v1/host/changes', { method: 'POST', body })).data.change;
  }

  /** This account's requests, newest first, optionally one status. */
  async list(options: { status?: HostChangeStatus } = {}): Promise<HostChangeList> {
    const query = options.status ? `?${new URLSearchParams({ status: options.status })}` : '';
    const res = await this.transport.request<{ ok: boolean; supported: boolean; changes: HostChange[] | null }>(`/v1/host/changes${query}`, { method: 'GET' });
    return { supported: res.data.supported === true, changes: res.data.changes ?? [] };
  }

  /** Owner only: applies the change through the helper; the returned record is `applied` or `failed`. */
  async approve(id: string, options: { reason?: string } = {}): Promise<HostChange> {
    return this.decide(id, 'approve', options.reason);
  }

  /** Owner only: refuses a pending request. */
  async deny(id: string, options: { reason?: string } = {}): Promise<HostChange> {
    return this.decide(id, 'deny', options.reason);
  }

  private async decide(id: string, decision: 'approve' | 'deny', reason?: string): Promise<HostChange> {
    if (!/^[A-Za-z0-9._-]+$/.test(id)) throw new Error('host change id is required');
    const body = reason ? { reason } : {};
    return (await this.transport.request<{ ok: boolean; change: HostChange }>(`/v1/host/changes/${encodeURIComponent(id)}/${decision}`, { method: 'POST', body })).data.change;
  }
}
