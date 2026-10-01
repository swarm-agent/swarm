import type { SwarmTransport } from './transport.js';
import type { WorkspaceRecord } from './types.js';

export class SwarmWorkspacesNamespace {
  private transport: SwarmTransport;

  constructor(transport: SwarmTransport) {
    this.transport = transport;
  }

  /**
   * Lists all workspaces registered with the daemon for the active principal.
   */
  async list(options: { limit?: number } = {}): Promise<WorkspaceRecord[]> {
    const q = new URLSearchParams();
    if (options.limit && options.limit > 0) {
      q.set('limit', options.limit.toString());
    }

    const queryStr = q.toString() ? `?${q.toString()}` : '';
    const res = await this.transport.request<{ ok: boolean; workspaces: WorkspaceRecord[] }>(
      `/v1/workspace/list${queryStr}`,
      { method: 'GET' }
    );
    const workspaces = res.data?.workspaces ?? [];
    return workspaces.map((w) => ({
      ...w,
      name: w.name || w.workspace_name || '',
      workspace_name: w.workspace_name || w.name || '',
    }));
  }

  /**
   * Retrieves the currently active workspace.
   */
  async current(): Promise<WorkspaceRecord | null> {
    const res = await this.transport.request<{ ok: boolean; workspace: WorkspaceRecord }>(
      '/v1/workspace/current',
      { method: 'GET' }
    );
    return res.data?.workspace ?? null;
  }

  /**
   * Resolves a host filesystem path to a registered workspace record.
   */
  async resolve(path: string): Promise<WorkspaceRecord> {
    const res = await this.transport.request<{ ok: boolean; workspace: WorkspaceRecord }>(
      '/v1/workspace/resolve',
      {
        method: 'POST',
        body: { path },
      }
    );
    return res.data.workspace;
  }
}
