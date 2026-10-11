import type { SwarmTransport } from './transport.js';
import type { WorkspaceRecord } from './types.js';

export class SwarmWorkspacesNamespace {
  private transport: SwarmTransport;

  constructor(transport: SwarmTransport) {
    this.transport = transport;
  }

  async createFolder(parent_path: string, name: string): Promise<WorkspaceFolder> {
    return (await this.transport.request<{ ok: boolean; folder: WorkspaceFolder }>('/v1/workspace/folders/create', { method: 'POST', body: { parent_path, name } })).data.folder;
  }
  /**
   * Creates `parent_path/name` as a new, empty Git repository with one initial
   * commit and registers it as a workspace, in one call. The daemon refuses a
   * folder that already holds other content. Calling it again for a folder
   * that is already registered returns that workspace.
   */
  async create(body: { parent_path: string; name: string; make_current?: boolean }): Promise<WorkspaceResolution> {
    if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$/.test(body.name)) {
      throw new Error('Workspace name must be letters, digits, dashes and underscores (at most 63).');
    }
    const parent = body.parent_path.replace(/\/+$/, '');
    if (!parent.startsWith('/')) throw new Error('parent_path must be absolute.');
    const path = `${parent}/${body.name}`;
    await this.setupRepository(path, path);
    return this.add({ path, name: body.name, make_current: body.make_current ?? false });
  }
  async add(body: { path: string; name?: string; theme_id?: string; make_current?: boolean; confirm_committed_only?: boolean }): Promise<WorkspaceResolution> {
    return (await this.transport.request<{ ok: boolean; workspace: WorkspaceResolution }>('/v1/workspace/add', { method: 'POST', body })).data.workspace;
  }
  async inspectRepository(path: string): Promise<WorkspaceRepository> {
    return (await this.transport.request<{ ok: boolean; repository: WorkspaceRepository }>(`/v1/workspace/repository?${new URLSearchParams({ path })}`)).data.repository;
  }
  async setupRepository(path: string, expected_resolved_path: string): Promise<WorkspaceRepository> {
    return (await this.transport.request<{ ok: boolean; repository: WorkspaceRepository }>('/v1/workspace/repository/setup', { method: 'POST', body: { path, expected_resolved_path } })).data.repository;
  }
  async reviewRepository(path: string): Promise<WorkspaceRepositoryReview> {
    return (await this.transport.request<{ ok: boolean; review: WorkspaceRepositoryReview }>(`/v1/workspace/repository/review?${new URLSearchParams({ path })}`)).data.review;
  }
  async baselineRepository(body: { path: string; expected_resolved_path: string; review_digest: string; selected_paths: string[]; confirm_baseline: boolean; confirm_omissions: boolean }): Promise<WorkspaceRepository> {
    return (await this.transport.request<{ ok: boolean; repository: WorkspaceRepository }>('/v1/workspace/repository/baseline', { method: 'POST', body })).data.repository;
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

export interface WorkspaceFolder { path: string; name: string; parent_path: string; requires_sudo: boolean; permission_error_message?: string }
export interface WorkspaceResolution {
  requested_path: string; resolved_path: string; workspace_path: string; workspace_name: string;
  workspace_id?: string; local_workspace_binding_id?: string; workspace_generation?: number; workspace_state?: string;
  [key: string]: unknown;
}
export interface WorkspaceRepository {
  state: string; path: string; repository_root?: string; head_commit?: string; can_setup: boolean;
  needs_review?: boolean; message: string; content_ready: boolean; runtime_accessible: boolean; actions?: string[];
}
export interface WorkspaceRepositoryReview {
  repository: WorkspaceRepository; digest: string; warning: string;
  files: { path: string; size: number; mode: number; digest: string; selectable: boolean }[];
}
