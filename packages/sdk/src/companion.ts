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
  routes: { session: string; messages: string; events: string; context: string };
  /** Live work state, read when `get` answers (absent from `ensure`). */
  activity?: CompanionActivity;
}

/**
 * The Companion's run, queued input, pending approvals and watched tasks, read
 * from the authoritative records. Refresh it on the Companion session's V3
 * events; it is not a second event stream.
 */
export interface CompanionActivity {
  run: {
    active: boolean;
    run_id?: string;
    status?: string;
    phase?: string;
    stop_reason?: string;
    updated_at?: number;
  };
  /**
   * The person's messages accepted while a run was busy that no model step has
   * received yet, oldest first. `queued`: the active run (or its follow-up)
   * reads it after its current tool call; a running command is not
   * interrupted. `waiting_next_turn`: its run was stopped; the next turn reads it.
   */
  queued_inputs: Array<{
    message_id: string;
    message_seq: number;
    created_at?: number;
    queued_for_run_id?: string;
    delivery_status: 'queued' | 'waiting_next_turn';
  }>;
  queued_inputs_more?: boolean;
  pending_permissions: number;
  /** Delegated tasks the Companion follows; titles from linked machines are untrusted. */
  task_watches: Array<{
    watch_id: string;
    project_id: string;
    machine?: string;
    until?: string;
    task_ids: string[];
    titles?: string[];
    created_at?: number;
  }>;
  read_at: number;
}

/** Provenance of a context bank record: the original message, or the work session it points to. */
export interface CompanionContextSource {
  kind: 'message' | 'session';
  session_id: string;
  message_seq?: number;
  /** Digest of the source message when it was recorded; a changed source invalidates the record. */
  content_sha256?: string;
}

/** One topic, decision or work pointer of the Companion's context bank. Separate from account memory. */
export interface CompanionContextRecord {
  id: string;
  kind: 'topic' | 'decision' | 'work';
  /** `invalidated` when its source was deleted or changed (see `invalid_reason`). */
  state: 'active' | 'invalidated';
  invalid_reason?: string;
  title: string;
  summary?: string;
  keywords: string[];
  workspace_path?: string;
  source: CompanionContextSource;
  revision: number;
  created_at: number;
  updated_at: number;
}

/** Pebble keys a bounded context query read. */
export interface CompanionContextReadStats {
  index_keys: number;
  records: number;
  sources: number;
}

export interface CompanionContextPage {
  ok: boolean;
  records: CompanionContextRecord[];
  /** Opaque; pass to `context.list` for the next page. Empty on the last page. */
  next_cursor: string;
  /** Records on this page bound to a workspace outside the Companion's current scope. */
  scope_hidden: number;
  read_stats: CompanionContextReadStats;
  content_trust: 'untrusted';
}

export interface CompanionContextSearchResult {
  ok: boolean;
  /** Verified records; `source_text` is untrusted data from the original source. */
  results: Array<{ record: CompanionContextRecord; score: number; source_text?: string }>;
  read_stats: CompanionContextReadStats;
  content_trust: 'untrusted';
}

export interface CompanionContextPut {
  id: string;
  /** Topics are indexed automatically from Companion messages. */
  kind: 'decision' | 'work';
  title: string;
  summary?: string;
  keywords?: string[];
  /** Must be inside the Companion's current workspace scope. */
  workspace_path?: string;
  source: CompanionContextSource;
  /** 0 to create; the current revision to replace. */
  expected_revision: number;
}

/**
 * The Companion's context bank: topics, decisions and work pointers with
 * their sources. Each Companion turn sees a bounded selection of it instead
 * of the whole conversation. It never changes account memory.
 */
export class SwarmCompanionContextNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  /** Newest first, at most 100 a page. */
  async list(params: { cursor?: string; limit?: number } = {}, signal?: AbortSignal): Promise<CompanionContextPage> {
    const query = new URLSearchParams();
    if (params.cursor) query.set('cursor', params.cursor);
    if (params.limit) query.set('limit', String(params.limit));
    const suffix = query.toString() ? `?${query}` : '';
    return (await this.transport.request<CompanionContextPage>(`/v3/companion/context${suffix}`, { method: 'GET', signal })).data;
  }

  /** Bounded keyword search (and/or one kind), verified against sources; at most 16 results. */
  async search(params: { q?: string; kind?: 'topic' | 'decision' | 'work'; limit?: number }, signal?: AbortSignal): Promise<CompanionContextSearchResult> {
    const query = new URLSearchParams();
    if (params.q) query.set('q', params.q);
    if (params.kind) query.set('kind', params.kind);
    if (params.limit) query.set('limit', String(params.limit));
    return (await this.transport.request<CompanionContextSearchResult>(`/v3/companion/context?${query}`, { method: 'GET', signal })).data;
  }

  /** Records a decision or work pointer; repeating an identical write changes nothing. */
  async put(record: CompanionContextPut): Promise<{ ok: boolean; changed: boolean; record: CompanionContextRecord }> {
    return (await this.transport.request<{ ok: boolean; changed: boolean; record: CompanionContextRecord }>('/v3/companion/context', { method: 'POST', body: record })).data;
  }

  /** Forgets one record at its current revision. */
  async delete(id: string, expectedRevision: number): Promise<{ ok: boolean; deleted_id: string }> {
    return (await this.transport.request<{ ok: boolean; deleted_id: string }>('/v3/companion/context', { method: 'DELETE', body: { id, expected_revision: expectedRevision } })).data;
  }
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
  /** The Companion's bounded, source-verified context bank. */
  readonly context: SwarmCompanionContextNamespace;

  constructor(private readonly transport: SwarmTransport) {
    this.context = new SwarmCompanionContextNamespace(transport);
  }

  /** The Companion, or null before it was first created. */
  async get(signal?: AbortSignal): Promise<CompanionView | null> {
    return (await this.transport.request<{ companion: CompanionView | null }>('/v3/companion', { method: 'GET', signal })).data.companion;
  }

  /** Get-or-create; idempotent. `workspace_path` is used only on first creation. */
  async ensure(params: { workspace_path?: string } = {}): Promise<CompanionEnsureResult> {
    return (await this.transport.request<CompanionEnsureResult>('/v3/companion', { method: 'POST', body: params })).data;
  }

  /** Deliberately deletes the Companion, its history and its context bank; the next `ensure` starts an empty one. */
  async delete(sessionId: string): Promise<CompanionDeleteResult> {
    if (!sessionId.trim()) throw new Error('The current Companion session_id is required');
    return (await this.transport.request<CompanionDeleteResult>('/v3/companion', { method: 'DELETE', body: { session_id: sessionId, confirm: true } })).data;
  }
}
