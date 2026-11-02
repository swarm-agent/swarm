import type { SwarmTransport } from './transport.js';

/** Lifecycle state of the account user's one Companion. */
export interface CompanionView {
  session_id: string;
  generation: number;
  /** `ready` once its session exists; `uncreated` after an explicit delete until the next `ensure`. */
  state: 'ready' | 'uncreated';
  title?: string;
  workspace_path?: string;
  created_at: number;
  updated_at: number;
  /** Canonical V3 routes carrying the Companion's history and live events. */
  routes: { session: string; messages: string; events: string };
}

export interface CompanionEnsureResult {
  ok: boolean;
  created: boolean;
  companion: CompanionView;
}

export interface CompanionDeleteResult {
  ok: boolean;
  deleted_session_id: string;
  companion: CompanionView;
}

/**
 * The account user's one persistent Companion: an ordinary Swarm session
 * (same tools, permissions and account model) that is not tied to a project
 * and cannot be archived, ended or deleted through chat controls. Use the
 * sessions/realtime namespaces with `companion.session_id` for messages.
 */
export class SwarmCompanionNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  /** The Companion, or null before it was first created. */
  async get(signal?: AbortSignal): Promise<CompanionView | null> {
    return (await this.transport.request<{ companion: CompanionView | null }>('/v3/companion', { method: 'GET', signal })).data.companion;
  }

  /** Get-or-create; idempotent. `workspace_path` is used only on first creation. */
  async ensure(params: { workspace_path?: string } = {}): Promise<CompanionEnsureResult> {
    return (await this.transport.request<CompanionEnsureResult>('/v3/companion', { method: 'POST', body: params })).data;
  }

  /** Deliberately deletes the Companion and its history; the next `ensure` starts an empty one. */
  async delete(sessionId: string): Promise<CompanionDeleteResult> {
    if (!sessionId.trim()) throw new Error('The current Companion session_id is required');
    return (await this.transport.request<CompanionDeleteResult>('/v3/companion', { method: 'DELETE', body: { session_id: sessionId, confirm: true } })).data;
  }
}
