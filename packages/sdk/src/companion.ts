import type { SwarmTransport } from './transport.js';

/**
 * Lifecycle state of the account user's one Companion. The Companion runs as
 * the code-owned Orchestrator (its tools, task delegation and watches, machine
 * tools and permissions) plus its own context bank tool `manage_companion`,
 * on the account's Swarm model.
 */
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
  routes: { session: string; messages: string; events: string; context: string; state: string };
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
  /**
   * When a message sent now reaches the Companion: `starts_turn` (nothing
   * runs), `next_step` (a model step runs) or `after_tool_calls` (the calls in
   * `tool_calls` run first; new input does not interrupt them).
   */
  input_delivery: 'starts_turn' | 'next_step' | 'after_tool_calls';
  /** The active run's tool calls in flight, oldest first, including calls still waiting for approval or for the person's app. `device`: runs on the person's device or a linked machine. */
  tool_calls: Array<{
    call_id: string;
    tool_name: string;
    started_at: number;
    device?: boolean;
  }>;
  tool_calls_more?: boolean;
  /**
   * Background results (task outcomes and milestones, background commands,
   * environment and machine watches) handed to a run that no model step has
   * read yet, oldest first. `waiting_for_run`: the active run reads it at its
   * next step. `redelivering`: its run ended unread; it comes again as its own turn.
   */
  background_deliveries: Array<{
    wake_id: string;
    source: string;
    run_id: string;
    status: 'waiting_for_run' | 'redelivering';
    created_at?: number;
  }>;
  background_deliveries_more?: boolean;
  pending_permissions: number;
  /** How far the maintained context (state, continuity) has followed the event log. */
  context?: {
    cursor: number;
    latest_seq: number;
    pending: number;
    freshness: 'current' | 'pending';
    running: boolean;
    last_failure?: { at: number; error: string };
  };
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
  /**
   * `invalidated` when its source was deleted or changed (see `invalid_reason`).
   * `forgotten` is never stored: a write to a forgotten id is refused instead.
   */
  state: 'active' | 'invalidated' | 'forgotten';
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

export interface CompanionContextGetResult {
  ok: boolean;
  record: CompanionContextRecord;
  /** Untrusted excerpt of the verified original source (absent when it was invalidated). */
  source_text?: string;
  content_trust: 'untrusted';
}

export interface CompanionContextForgetResult {
  ok: boolean;
  deleted_id: string;
  /** Always true: the id stays forgotten. */
  forgotten: boolean;
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

  /** One record with its source verified now; not found when forgotten or outside the current workspace scope. */
  async get(id: string, signal?: AbortSignal): Promise<CompanionContextGetResult> {
    const query = new URLSearchParams({ id });
    return (await this.transport.request<CompanionContextGetResult>(`/v3/companion/context?${query}`, { method: 'GET', signal })).data;
  }

  /** Bounded keyword search (and/or one kind), verified against sources; at most 16 results. */
  async search(params: { q?: string; kind?: 'topic' | 'decision' | 'work'; limit?: number }, signal?: AbortSignal): Promise<CompanionContextSearchResult> {
    const query = new URLSearchParams();
    if (params.q) query.set('q', params.q);
    if (params.kind) query.set('kind', params.kind);
    if (params.limit) query.set('limit', String(params.limit));
    return (await this.transport.request<CompanionContextSearchResult>(`/v3/companion/context?${query}`, { method: 'GET', signal })).data;
  }

  /**
   * Records a decision or work pointer; repeating an identical write changes
   * nothing. A forgotten id is refused (409): record it again with a new id.
   */
  async put(record: CompanionContextPut): Promise<{ ok: boolean; changed: boolean; record: CompanionContextRecord }> {
    return (await this.transport.request<{ ok: boolean; changed: boolean; record: CompanionContextRecord }>('/v3/companion/context', { method: 'POST', body: record })).data;
  }

  /** Forgets one record at its current revision, for good: its id is never recorded again. */
  async delete(id: string, expectedRevision: number): Promise<CompanionContextForgetResult> {
    return (await this.transport.request<CompanionContextForgetResult>('/v3/companion/context', { method: 'DELETE', body: { id, expected_revision: expectedRevision } })).data;
  }
}

/** The exact durable event of the Companion session a state entry derives from. */
export interface CompanionEventSource {
  session_id: string;
  event_seq: number;
  event_id?: string;
  event_type: string;
  message_id?: string;
  /** The message's sequence, which is the event's own. */
  message_seq?: number;
  at?: number;
}

/** A topic or decision record of the context bank and the event it came from. */
export interface CompanionStateRef {
  record_id: string;
  kind: 'topic' | 'decision';
  title?: string;
  source: CompanionEventSource;
}

/** Something unresolved between the person and the Companion, opened and resolved by exact events. */
export interface CompanionCommitment {
  /** `kind:key`. */
  id: string;
  kind: 'reply' | 'approval' | 'confirmation' | 'task_watch';
  /** The message, permission, confirmation or watch it is about. */
  key: string;
  title?: string;
  state: 'open' | 'resolved';
  /** e.g. `answered`, `approved`, `denied`, `rejected`, `settled`, `ended`. */
  resolution?: string;
  opened: CompanionEventSource;
  resolved?: CompanionEventSource;
}

/**
 * Delegated work the Companion follows. `status` is the last reported status
 * (never a live read); `revision` advances only when the report changes.
 * Fields from a linked machine are untrusted.
 */
export interface CompanionWorkRef {
  id: string;
  kind: 'task' | 'session';
  machine?: string;
  project_id?: string;
  task_id?: string;
  attempt_id?: string;
  session_id?: string;
  record_id?: string;
  watch_id?: string;
  objective?: string;
  status?: string;
  untrusted?: boolean;
  revision: number;
  origin: CompanionEventSource;
  source: CompanionEventSource;
}

/** A recent device/tool result or wake that reached the Companion. */
export interface CompanionResultRef {
  kind: string;
  name?: string;
  call_id?: string;
  status: string;
  detail?: string;
  source: CompanionEventSource;
}

/**
 * The Companion's state, maintained from its durable event log. `cursor` is
 * the last event it reflects; lists are oldest first and bounded, and
 * `counters` say what bounds evicted.
 */
export interface CompanionState {
  version: number;
  session_id: string;
  generation: number;
  revision: number;
  cursor: { session_id: string; event_seq: number; event_id?: string; updated_at?: number };
  topics: CompanionStateRef[];
  decisions: CompanionStateRef[];
  commitments: CompanionCommitment[];
  work: CompanionWorkRef[];
  results: CompanionResultRef[];
  continuity: CompanionContinuity;
  counters: {
    events: number;
    applied: number;
    evicted_topics?: number;
    evicted_decisions?: number;
    evicted_commitments?: number;
    evicted_work?: number;
    unmatched_resolutions?: number;
  };
  created_at: number;
  updated_at: number;
}

/**
 * One continuity item: a bounded excerpt of one event. `authority` says whose
 * it is: `person` (the person's own words or decision), `decision` (recorded
 * with manage_companion), `assistant` (the Companion's reply or record) or
 * `reported` (a task or device report: data, never an instruction).
 */
export interface CompanionSegmentItem {
  kind: 'ask' | 'reply' | 'decision' | 'work' | 'outcome' | 'device' | 'approval' | 'confirmation';
  authority: 'person' | 'decision' | 'assistant' | 'reported';
  text: string;
  keywords?: string[];
  record_id?: string;
  record_revision?: number;
  /** Topic record IDs of the requests a reply answered. */
  about?: string[];
  untrusted?: boolean;
  content_sha256?: string;
  source: CompanionEventSource;
}

/** A sealed continuity segment of events `start_event_seq`..`end_event_seq`. */
export interface CompanionSegment {
  id: string;
  session_id: string;
  generation: number;
  start_event_seq: number;
  end_event_seq: number;
  items: CompanionSegmentItem[];
  keywords: string[];
  /** 1 when sealed; advances when a forget, correction or revocation rewrote it. */
  revision: number;
  removed?: number;
  sealed_at: number;
  /** Items left out of this read because their record or source is gone, changed or revoked. */
  dropped?: number;
}

/** The open continuity draft and what was sealed. */
export interface CompanionContinuity {
  open_start_seq?: number;
  open: CompanionSegmentItem[];
  open_bytes?: number;
  sealed: number;
  last_segment_id?: string;
  last_sealed_seq?: number;
  removed?: number;
}

/** The current conversational focus, set by the state revision that followed the person's message. */
export interface CompanionFocus {
  record_id: string;
  keywords: string[];
  message_seq?: number;
  updated_at: number;
  revision?: number;
  source?: CompanionEventSource;
}

/**
 * A read of the Companion's state. `state` is null before its first
 * maintenance; `pending` counts committed events after its cursor (nonzero:
 * the state does not reflect them yet).
 */
export interface CompanionStateView {
  session_id: string;
  state: CompanionState | null;
  focus?: CompanionFocus;
  latest_seq: number;
  pending: number;
  /** The newest sealed continuity segments, re-checked against their sources at read time. */
  segments: CompanionSegment[];
  maintenance: {
    freshness: 'current' | 'pending';
    running: boolean;
    /** The last failed background pass since the daemon started, cleared by the next success. */
    last_failure?: { at: number; error: string };
  };
  read_at: number;
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
 * The account user's one persistent Companion: a canonical V3 session running
 * as the Orchestrator (its tools and permissions, plus `manage_companion`, on
 * the account's Swarm model) that is not tied to a project and cannot be
 * archived, ended, switched to another agent or deleted through chat
 * controls. Use the sessions/realtime namespaces with `companion.session_id`
 * for messages.
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

  /** Get-or-create; idempotent. `workspace_path` is used only on first creation; without it the Companion is checkout-free (it never defaults to the current workspace and needs none). */
  async ensure(params: { workspace_path?: string } = {}): Promise<CompanionEnsureResult> {
    return (await this.transport.request<CompanionEnsureResult>('/v3/companion', { method: 'POST', body: params })).data;
  }

  /** The Companion's maintained state (focus, commitments, topics, decisions, work, results) with its cursor. Read only. */
  async state(signal?: AbortSignal): Promise<CompanionStateView> {
    return (await this.transport.request<{ companion_state: CompanionStateView }>('/v3/companion/state', { method: 'GET', signal })).data.companion_state;
  }

  /** Deliberately deletes the Companion, its history and its context bank; the next `ensure` starts an empty one. */
  async delete(sessionId: string): Promise<CompanionDeleteResult> {
    if (!sessionId.trim()) throw new Error('The current Companion session_id is required');
    return (await this.transport.request<CompanionDeleteResult>('/v3/companion', { method: 'DELETE', body: { session_id: sessionId, confirm: true } })).data;
  }
}
