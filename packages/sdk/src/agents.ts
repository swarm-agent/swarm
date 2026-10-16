import { SwarmApiError } from './errors.js';
import type { PendingPermissionRecord } from './permissions.js';
import { SwarmSessionsNamespace } from './sessions.js';
import type { SwarmTransport } from './transport.js';

/** What a wait returned for. */
export type SessionWaitReason = 'assistant_message' | 'tool_call' | 'run_ended' | 'session_ended' | 'confirmation';

/** One answer from `agents.wait`: why it returned and the session's state now. */
export interface SessionWait {
  sessionId: string;
  /** Pass back as `after` to wait for what comes next. */
  cursor: number;
  /** Empty when the wait ended without anything to act on (see `timedOut`). */
  reasons: SessionWaitReason[];
  timedOut: boolean;
  /** More events follow the cursor: wait again at once. */
  more: boolean;
  /** The cursor was ahead of the session; `cursor` is where it is now. */
  staleCursor: boolean;
  /** The conversation was ended (`endSession`): it takes no more messages. */
  ended: boolean;
  run?: { run_id: string; status: string; active: boolean };
  /** Tool calls waiting for an answer, as `GET .../permissions?status=pending` returns them. */
  pendingCalls: PendingPermissionRecord[];
  /** Confirmed calls waiting for the person's approval (at most one); reason `confirmation` wakes a wait. */
  pendingConfirmations: PendingConfirmation[];
  /** The session's end user and labels, from the stored session (gateway conversations). */
  endUser?: string;
  labels?: Record<string, string>;
}

/** One of an agent's conversations, as `agents.listSessions` returns it. */
export interface AgentSession {
  id: string;
  title: string;
  created_at: number;
  updated_at: number;
  message_count: number;
  last_message_at: number;
  state: 'active' | 'ended';
  ended?: { at: number; by: string; reason?: string };
  takeover?: Takeover;
  run_active: boolean;
  /** Set on gateway conversations, from the stored session. */
  end_user?: string;
  labels?: Record<string, string>;
}

export interface ListAgentSessionsParams {
  /** Owner only: the agent to list. A gateway token always lists its own agent. */
  agent?: string;
  state?: 'active' | 'ended' | 'all';
  /** Unix milliseconds. */
  updatedAfter?: number;
  updatedBefore?: number;
  /** `nextCursor` from the previous page. */
  cursor?: string;
  /** 1 to 200 (default 50). */
  limit?: number;
}

export type AgentEventKind =
  | 'session.created' | 'message.user' | 'message.assistant'
  | 'tool.called' | 'tool.answered' | 'tool.failed'
  | 'session.ended' | 'run.failed' | 'takeover.started' | 'takeover.ended'
  | 'confirmation.requested' | 'confirmation.decided';

/**
 * One entry in an agent's event feed. Message text and tool arguments,
 * results and reasons are cut to 2000 characters, with `data.truncated` set.
 */
export interface AgentEvent {
  /** Feed position; pass the last `next_after` back as `after`. */
  seq: number;
  id: string;
  at: number;
  kind: AgentEventKind;
  session_id: string;
  /** The conversation's end user and labels (gateway conversations), from the stored session. */
  end_user?: string;
  labels?: Record<string, string>;
  data?: Record<string, unknown>;
}

export interface AgentEventPage {
  events: AgentEvent[];
  next_after: number;
  latest_seq: number;
  oldest_seq: number;
  /** Entries after your cursor were already dropped (or the cursor is ahead): resync from `next_after`. */
  gap: boolean;
}

export interface AgentEventsParams {
  /** Owner only: the agent to follow. A gateway or operator token always follows its own agent. */
  agent?: string;
  after?: number;
  /** 1 to 500 (default 100). */
  limit?: number;
  /** Kind prefixes, e.g. `['message', 'tool.failed']`. */
  kinds?: string[];
  /** Wait up to this many seconds (max 30) for an entry when there is none. */
  waitSeconds?: number;
  signal?: AbortSignal;
}

/** Yielded by `watchEvents` when entries were missed (dropped by feed retention). */
export interface AgentEventGap { kind: 'gap'; from: number; to: number }

const WAIT_SECONDS = 25;

// Exactly what daemons released before GET /v3/sessions/{id}/wait answer for
// that route: 404, or these unknown-route refusals. Anything else is a real error.
const OLDER_DAEMON_WAIT_REFUSALS = new Set([
  'unknown sessions v3 path',
  'route unavailable to an agent-bound token',
  'route unavailable to an operator token',
  'route unavailable on container SDK listener',
]);

function daemonHasNoWait(error: unknown): boolean {
  if (!(error instanceof SwarmApiError)) return false;
  if (error.status === 404) return error.message !== 'session not found';
  return (error.status === 400 || error.status === 403) && OLDER_DAEMON_WAIT_REFUSALS.has(error.message);
}

/** The pending records that are calls to the tools you answer, with parsed arguments. */
function clientToolCalls(sessionId: string, records: PendingPermissionRecord[], tools: Iterable<string>, owner: { endUser?: string; labels?: Record<string, string> }): ClientToolCall[] {
  const names = new Set(tools);
  const calls: ClientToolCall[] = [];
  for (const record of records) {
    if (!names.has(record.tool_name) || record.session_id !== sessionId) continue;
    let args: unknown;
    try { args = JSON.parse(record.tool_call_arguments || record.tool_arguments || '{}'); } catch { args = null; }
    if (!args || typeof args !== 'object' || Array.isArray(args)) continue;
    calls.push({ kind: 'call', id: record.id, sessionId, runId: record.run_id, tool: record.tool_name, arguments: args as Record<string, unknown>, createdAt: record.created_at, ...owner });
  }
  return calls;
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise<void>((resolve) => {
    const finish = () => { clearTimeout(timer); signal?.removeEventListener('abort', finish); resolve(); };
    const timer = setTimeout(finish, ms);
    signal?.addEventListener('abort', finish, { once: true });
  });
}

/** A typed tool the application answers. Swarm validates and waits; it never executes it. */
export interface ClientToolDefinition {
  name: string;
  description: string;
  /** JSON Schema for the arguments; must be `type: "object"` with only local `$ref`s. */
  input_schema: Record<string, unknown>;
  /** `write` marks a tool with external effects. Write tools are confirmed by default (see `confirm`). */
  effect?: 'read' | 'write';
  /** How long a call waits for an answer before the model gets "client tool timed out" (max 300000). */
  timeout_ms?: number;
  /**
   * Each call waits for the person to approve it before your handler runs it.
   * Default: true for write tools, false for read tools. The model must add a
   * one-sentence `summary` argument for the approval card; your handler never
   * receives it.
   */
  confirm?: boolean;
  /** How long a call waits for the person's decision before the model is told nobody answered (default 900000 = 15 min, max 1 day). */
  confirm_timeout_ms?: number;
}

/**
 * An agent whose only capabilities are the client tools listed. It has no
 * built-in tools: no shell, files, web or delegation. Swarm enforces this
 * when each call executes, not only in what the model is shown.
 */
export interface SealedAgentDefinition {
  name: string;
  prompt: string;
  description?: string;
  tools: string[];
  provider?: string;
  model?: string;
  thinking?: string;
  /**
   * Bounds one message's cost. Unset fields use the sealed defaults (4 model calls, 1024 output tokens, last 40 messages, 60 s).
   * `retention_days` (1 to 3650; unset keeps them) deletes ended or idle conversations that many days after their last activity.
   */
  limits?: { max_steps?: number; max_output_tokens?: number; max_history_messages?: number; run_timeout_ms?: number; retention_days?: number };
}

/** The account's spend today and its daily cap, as the daemon enforces it. */
export interface SpendToday {
  today_cost_usd: number;
  daily_cost_limit_usd: number;
  enabled: boolean;
  limit_exceeded: boolean;
}

/** A pending client tool call your application must answer. */
export interface ClientToolCall {
  kind: 'call';
  /** Pass to `answer`/`fail`. */
  id: string;
  sessionId: string;
  runId: string;
  tool: string;
  /** Already validated by Swarm against the tool's input_schema. */
  arguments: Record<string, unknown>;
  createdAt: number;
  /**
   * The session's end user, as your gateway set it at create time. It comes
   * from the stored session, never from the model: decide whose data the call
   * may see from this, not from `arguments`.
   */
  endUser?: string;
  labels?: Record<string, string>;
}

/** A gateway token's limits; omitted fields keep the daemon defaults (or, on update, their current value). */
export interface GatewayTokenLimits {
  /** Messages (model runs) the token may start per minute (default 120). */
  messagesPerMinute?: number;
  /** New conversations per hour (default 300). */
  sessionsPerHour?: number;
  /** Model runs at once across the agent's conversations (default 4, 1-64). */
  maxConcurrentRuns?: number;
  /** Messages per minute for one end user's conversations (default 20). */
  endUserMessagesPerMinute?: number;
  /** New conversations per hour for one end user (default 30). */
  endUserSessionsPerHour?: number;
  /** Model tokens one end user may use per UTC day (default: no cap). */
  endUserMaxTokensPerDay?: number;
}

/** One end user's model usage for a UTC day, from the provider receipts of their sessions. */
export interface EndUserUsage {
  endUser: string;
  /** False when nothing was recorded for them that day. */
  recorded: boolean;
  usage: { total_tokens: number; input_tokens: number; output_tokens: number; receipt_count: number } & Record<string, unknown>;
}

/**
 * `code` on a 429 from a gateway token: `token_*` is the token's own limit,
 * `end_user_*` one end user's. Retry after `Retry-After` seconds.
 */
export type GatewayLimitCode =
  | 'token_messages_rate_limited'
  | 'token_sessions_rate_limited'
  | 'token_concurrent_runs_limited'
  | 'end_user_messages_rate_limited'
  | 'end_user_sessions_rate_limited'
  | 'end_user_daily_tokens_exceeded'
  | 'rate_limit_capacity_reached';

const LIMIT_FIELDS: Array<[keyof GatewayTokenLimits, string]> = [
  ['messagesPerMinute', 'messages_per_minute'],
  ['sessionsPerHour', 'sessions_per_hour'],
  ['maxConcurrentRuns', 'max_concurrent_runs'],
  ['endUserMessagesPerMinute', 'end_user_messages_per_minute'],
  ['endUserSessionsPerHour', 'end_user_sessions_per_hour'],
  ['endUserMaxTokensPerDay', 'end_user_max_tokens_per_day'],
];

function limitBody(limits: GatewayTokenLimits, keepZero: boolean): Record<string, number> {
  const body: Record<string, number> = {};
  for (const [key, field] of LIMIT_FIELDS) {
    const value = limits[key];
    if (value === undefined || (!keepZero && !value)) continue;
    if (!Number.isInteger(value) || value < 0) throw new Error(`${key} must be a non-negative integer`);
    body[field] = value;
  }
  return body;
}

function endUserFields(data: Record<string, unknown> | undefined): { endUser?: string; labels?: Record<string, string> } {
  const out: { endUser?: string; labels?: Record<string, string> } = {};
  if (typeof data?.end_user === 'string') out.endUser = data.end_user;
  if (data?.labels && typeof data.labels === 'object' && !Array.isArray(data.labels)) out.labels = data.labels as Record<string, string>;
  return out;
}

/**
 * A call to a confirmed client tool, waiting for the person's approval. Show
 * `summary` and `arguments` on a card, then call `confirm` or `reject`.
 * Nothing has run yet; after approval the same call reaches your handler with
 * exactly these `arguments`.
 */
export interface PendingConfirmation {
  kind: 'confirmation';
  /** Pass to `confirm`/`reject`/`decideConfirmation`. */
  id: string;
  sessionId: string;
  runId: string;
  tool: string;
  /** The model's one-sentence description of what approving will do. */
  summary: string;
  /** What your handler will receive on approval (validated against the tool's schema). */
  arguments: Record<string, unknown>;
  /** `sha256:<hex>` of the arguments; an approval must send it back. */
  argumentsHash: string;
  createdAt: number;
  /** After this (unix ms) the model is told nobody answered. */
  expiresAt: number;
  /** The session's end user and labels, from the stored session (as on `ClientToolCall`). */
  endUser?: string;
  labels?: Record<string, string>;
}

/** A confirmation as the daemon stores it, in any state. */
export interface ClientToolConfirmationRecord {
  id: string;
  kind: 'confirmation';
  status: 'pending' | 'approved' | 'rejected' | 'expired';
  tool: string;
  summary: string;
  arguments: Record<string, unknown>;
  arguments_hash: string;
  run_id: string;
  call_id: string;
  created_at: number;
  expires_at: number;
  decided_at?: number;
  note?: string;
  execution_call_id: string;
  execution_permission_id?: string;
  execution_deadline?: number;
  delivered?: boolean;
  delivered_at?: number;
  outcome_message_id?: string;
  continuation_run_id?: string;
}

/** Called once for each confirmation `serve` sees: show the approval card from here. */
export type ConfirmationHandler = (confirmation: PendingConfirmation) => Promise<void> | void;

function pendingConfirmation(sessionId: string, record: ClientToolConfirmationRecord, owner: { endUser?: string; labels?: Record<string, string> }): PendingConfirmation {
  return {
    kind: 'confirmation', id: record.id, sessionId, runId: record.run_id, tool: record.tool, summary: record.summary,
    arguments: record.arguments ?? {}, argumentsHash: record.arguments_hash, createdAt: record.created_at, expiresAt: record.expires_at,
    ...owner,
  };
}

/** Throw from a handler to send this message to the model; other errors send "tool failed". */
export class ClientToolError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ClientToolError';
  }
}

export type ClientToolHandler = (args: Record<string, unknown>, call: ClientToolCall) => Promise<unknown> | unknown;

/** One visitor turn: the agent's reply and every message the turn added (including tool results). */
export interface ConverseResult {
  reply: string;
  messages: Array<Record<string, any>>;
  /**
   * Set when the turn ended waiting for the person to approve a confirmed
   * tool call: show it, then call `confirm` or `reject` (each returns the
   * agent's next reply). `reply` holds whatever the agent said before asking.
   */
  confirmation?: PendingConfirmation;
}

/** Options of `serve`. */
export interface ServeOptions {
  signal?: AbortSignal;
  /** Poll interval, only against a daemon that predates waiting. */
  intervalMs?: number;
  /** Fail calls (and hide confirmations) of sessions that belong to anyone else. */
  endUser?: string;
  onConfirm?: ConfirmationHandler;
}

/** Options of `converse`, `confirm` and `reject`. */
export interface TurnOptions {
  timeoutMs?: number;
  /** Poll interval, only against a daemon that predates waiting. */
  pollIntervalMs?: number;
  /** Refuse a session that belongs to anyone else before sending anything. */
  endUser?: string;
  onConfirm?: ConfirmationHandler;
}

const NAME = /^[a-z][a-z0-9_]{0,63}$/;

function toolName(name: string): string {
  if (typeof name !== 'string' || !NAME.test(name)) throw new Error('tool and agent names are lowercase letters, digits and underscores');
  return name;
}

/** Who holds a conversation, as stored in its metadata under `takeover`. */
export type Takeover = { state: 'human'; by: string; since: number } | { state: 'agent'; ended_at: number };

function requestId(prefix: string): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return `${prefix}-${crypto.randomUUID()}`;
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).substring(2, 10)}`;
}

function operatorRequestId(): string {
  return requestId('sdk-operator');
}

/**
 * Sealed agents and client tools (`/v2/agents`, `/v2/custom-tools`). Defining
 * them needs the owner or an `admin` token: keep this in a trusted backend.
 */
export class SwarmAgentsNamespace {
  private olderDaemonNoted = false;

  constructor(private readonly transport: SwarmTransport) {}

  /** Records, once, that this daemon predates waiting and the SDK polls instead. */
  private noteOlderDaemon(): void {
    if (this.olderDaemonNoted) return;
    this.olderDaemonNoted = true;
    console.debug('swarm sdk: this daemon has no GET /v3/sessions/{id}/wait (it predates it); serve and converse poll instead');
  }

  async defineClientTool(definition: ClientToolDefinition): Promise<Record<string, unknown>> {
    const name = toolName(definition.name);
    const res = await this.transport.request<{ custom_tool: Record<string, unknown> }>(`/v2/custom-tools/${name}`, {
      method: 'PUT',
      body: {
        kind: 'client',
        description: definition.description,
        input_schema: definition.input_schema,
        effect: definition.effect ?? 'read',
        ...(definition.timeout_ms !== undefined ? { timeout_ms: definition.timeout_ms } : {}),
        ...(definition.confirm !== undefined ? { confirm: definition.confirm } : {}),
        ...(definition.confirm_timeout_ms !== undefined ? { confirm_timeout_ms: definition.confirm_timeout_ms } : {}),
      },
    });
    return res.data.custom_tool;
  }

  async defineSealedAgent(definition: SealedAgentDefinition): Promise<Record<string, unknown>> {
    const name = toolName(definition.name);
    if (!Array.isArray(definition.tools)) throw new Error('tools must be an array of client tool names');
    const tools: Record<string, { enabled: true }> = {};
    for (const tool of definition.tools) tools[toolName(tool)] = { enabled: true };
    const res = await this.transport.request<{ profile: Record<string, unknown> }>(`/v2/agents/${name}`, {
      method: 'PUT',
      body: {
        mode: 'subagent',
        description: definition.description ?? '',
        prompt: definition.prompt,
        ...(definition.provider ? { provider: definition.provider } : {}),
        ...(definition.model ? { model: definition.model } : {}),
        ...(definition.thinking ? { thinking: definition.thinking } : {}),
        ...(definition.limits ? { limits: definition.limits } : {}),
        tool_contract: { preset: 'custom', tools },
      },
    });
    return res.data.profile;
  }

  /**
   * Mints the token a gateway holds to serve one sealed agent. It can only
   * create and use that agent's sessions and answer its client tool calls;
   * it stops working if the agent is given any built-in tool. Owner only.
   * The daemon requires an expiry of at most 30 days (2592000 seconds).
   */
  async createGatewayToken(agentName: string, options: { name?: string; expiresInSeconds: number } & GatewayTokenLimits): Promise<{ token: string; record: Record<string, unknown> }> {
    const agent = toolName(agentName);
    const res = await this.transport.request<{ token: string; record: Record<string, unknown> }>('/v3/auth/tokens', {
      method: 'POST',
      body: {
        name: options.name ?? `${agent} gateway`, agent_name: agent,
        expires_in_seconds: options.expiresInSeconds,
        ...limitBody(options, false),
      },
    });
    return { token: res.data.token, record: res.data.record };
  }

  /**
   * Changes a gateway token's limits in place (owner only); fields you omit
   * keep their value, 0 restores the default. Applies from its next request.
   */
  async updateGatewayTokenLimits(tokenId: string, limits: GatewayTokenLimits): Promise<Record<string, unknown>> {
    if (typeof tokenId !== 'string' || !tokenId) throw new Error('tokenId is required');
    const body = limitBody(limits, true);
    if (Object.keys(body).length === 0) throw new Error('give at least one limit to change');
    const res = await this.transport.request<{ record: Record<string, unknown> }>(`/v3/auth/tokens/${encodeURIComponent(tokenId)}/limits`, { method: 'POST', body });
    return res.data.record;
  }

  /**
   * Creates a conversation with a sealed agent for one end user (gateway
   * token). `endUser` is stored with the session once and cannot change; the
   * model never sees it. Pass the same `clientRequestId` to retry safely.
   */
  async startConversation(agentName: string, options: { endUser?: string; labels?: Record<string, string>; title?: string; clientRequestId?: string; workspacePath?: string; workspaceId?: string } = {}): Promise<{ sessionId: string; endUser?: string; labels?: Record<string, string> }> {
    const agent = toolName(agentName);
    const session = await new SwarmSessionsNamespace(this.transport).create({
      agent_name: agent,
      ...(options.title !== undefined ? { title: options.title } : {}),
      ...(options.clientRequestId !== undefined ? { client_request_id: options.clientRequestId } : {}),
      ...(options.workspacePath !== undefined ? { workspace_path: options.workspacePath } : {}),
      ...(options.workspaceId !== undefined ? { workspace_id: options.workspaceId } : {}),
      ...(options.endUser !== undefined ? { end_user: options.endUser } : {}),
      ...(options.labels !== undefined ? { labels: options.labels } : {}),
    });
    if (options.endUser !== undefined && session.end_user !== options.endUser) {
      throw new Error('the daemon did not confirm the conversation\'s end user');
    }
    return { sessionId: session.id, ...(session.end_user !== undefined ? { endUser: session.end_user } : {}), ...(session.labels ? { labels: session.labels } : {}) };
  }

  /**
   * One UTC day's model usage of the agent's conversations, by end user (a
   * gateway token reads its own agent; the owner passes `agentName`).
   */
  async endUserUsage(options: { endUser?: string; date?: string; limit?: number; agentName?: string } = {}): Promise<{ date: string; endUsers: EndUserUsage[]; truncated: boolean }> {
    const query = new URLSearchParams();
    if (options.agentName) query.set('agent_name', toolName(options.agentName));
    if (options.endUser !== undefined) query.set('end_user', options.endUser);
    if (options.date) query.set('date', options.date);
    if (options.limit !== undefined) query.set('limit', String(options.limit));
    const suffix = query.toString() ? `?${query.toString()}` : '';
    const res = await this.transport.request<{ date: string; truncated: boolean; end_users: Array<{ end_user: string; recorded: boolean; usage: EndUserUsage['usage'] }> }>(`/v3/sessions:end-user-usage${suffix}`);
    return {
      date: res.data.date,
      truncated: Boolean(res.data.truncated),
      endUsers: (res.data.end_users ?? []).map((row) => ({ endUser: row.end_user, recorded: row.recorded, usage: row.usage })),
    };
  }

  /** Today's spend against the daily cap. A gateway token may read it. */
  /**
   * Mints the human-takeover token for one sealed agent: it can read that
   * agent's conversations, take one over (the agent stops replying), reply as a
   * person and hand it back. It cannot post as the visitor, start runs or answer
   * tool calls. Owner only; the daemon requires an expiry of at most 30 days.
   */
  async createOperatorToken(agentName: string, options: { name?: string; expiresInSeconds: number }): Promise<{ token: string; record: Record<string, unknown> }> {
    const agent = toolName(agentName);
    const res = await this.transport.request<{ token: string; record: Record<string, unknown> }>('/v3/auth/tokens', {
      method: 'POST',
      body: { name: options.name ?? `${agent} operator`, agent_name: agent, operator: true, expires_in_seconds: options.expiresInSeconds },
    });
    return { token: res.data.token, record: res.data.record };
  }

  /** Who holds a conversation: `{ state: 'human', by, since }`, `{ state: 'agent', ended_at }`, or undefined. */
  async takeoverState(sessionId: string): Promise<Takeover | undefined> {
    const res = await this.transport.request<{ takeover?: Takeover }>(`/v3/sessions/${encodeURIComponent(sessionId)}/takeover`);
    return res.data.takeover ?? undefined;
  }

  /** A person takes the conversation: the agent's run in flight stops and visitor messages start no runs. */
  async takeOver(sessionId: string, options: { clientRequestId?: string } = {}): Promise<Takeover> {
    return this.setTakeover(sessionId, 'start', options.clientRequestId);
  }

  /** Hands the conversation back to the agent; the next visitor message runs it again. */
  async handBack(sessionId: string, options: { clientRequestId?: string } = {}): Promise<Takeover> {
    return this.setTakeover(sessionId, 'end', options.clientRequestId);
  }

  /** Replies as a person during a takeover (an assistant message with source "operator"). */
  async replyAsOperator(sessionId: string, content: string, options: { clientRequestId?: string } = {}): Promise<Record<string, unknown>> {
    const res = await this.transport.request<{ message: Record<string, unknown> }>(`/v3/sessions/${encodeURIComponent(sessionId)}/operator-messages`, {
      method: 'POST',
      body: { content, client_request_id: options.clientRequestId ?? operatorRequestId() },
    });
    return res.data.message;
  }

  private async setTakeover(sessionId: string, action: 'start' | 'end', clientRequestId?: string): Promise<Takeover> {
    const res = await this.transport.request<{ takeover: Takeover }>(`/v3/sessions/${encodeURIComponent(sessionId)}/takeover`, {
      method: 'POST',
      body: { action, client_request_id: clientRequestId ?? operatorRequestId() },
    });
    return res.data.takeover;
  }

  async spendToday(): Promise<SpendToday> {
    const res = await this.transport.request<{ limits: SpendToday }>('/v3/sessions:usage-limits');
    return res.data.limits;
  }

  async removeAgent(name: string): Promise<void> {
    await this.transport.request(`/v2/agents/${toolName(name)}`, { method: 'DELETE' });
  }

  async removeClientTool(name: string): Promise<void> {
    await this.transport.request(`/v2/custom-tools/${toolName(name)}`, { method: 'DELETE' });
  }

  /**
   * Waits on one conversation (`GET /v3/sessions/{id}/wait`). Without `after`
   * it answers at once with the current state and cursor. With `after` it
   * returns as soon as there is a new assistant message, a pending tool call,
   * a confirmation waiting for the person, the end of a run or of the
   * session, or after `waitSeconds` (default 25, max 30). A gateway token may
   * wait only on its agent's sessions.
   */
  async wait(sessionId: string, options: { after?: number; waitSeconds?: number; signal?: AbortSignal } = {}): Promise<SessionWait> {
    const q = new URLSearchParams();
    let seconds = 0;
    if (typeof options.after === 'number') {
      q.set('after', String(options.after));
      seconds = Math.max(0, Math.min(30, Math.floor(options.waitSeconds ?? WAIT_SECONDS)));
      q.set('wait_seconds', String(seconds));
    }
    const path = `/v3/sessions/${encodeURIComponent(sessionId)}/wait${q.toString() ? `?${q.toString()}` : ''}`;
    const res = await this.transport.request<{
      session_id: string; cursor: number; reasons?: SessionWaitReason[] | null; timed_out?: boolean; more?: boolean;
      stale_cursor?: boolean; ended?: boolean; run?: SessionWait['run']; pending_calls?: PendingPermissionRecord[] | null;
      pending_confirmations?: ClientToolConfirmationRecord[] | null; end_user?: string; labels?: Record<string, string>;
    }>(path, { signal: options.signal, timeoutMs: (seconds + 15) * 1000 });
    const d = res.data;
    const owner = endUserFields(d as Record<string, unknown>);
    return {
      sessionId: d.session_id ?? sessionId, cursor: d.cursor, reasons: d.reasons ?? [], timedOut: !!d.timed_out,
      more: !!d.more, staleCursor: !!d.stale_cursor, ended: !!d.ended, run: d.run ?? undefined, pendingCalls: d.pending_calls ?? [],
      pendingConfirmations: (d.pending_confirmations ?? []).filter((record) => record.status === 'pending').map((record) => pendingConfirmation(sessionId, record, owner)),
      ...owner,
    };
  }

  /**
   * What waits on your application in a session, limited to the tool names you
   * answer: client tool calls to run (`kind: 'call'`) and confirmed calls
   * waiting for the person's approval (`kind: 'confirmation'`). Both carry the
   * session's `endUser` and `labels`.
   */
  async pendingCalls(sessionId: string, tools: Iterable<string>): Promise<Array<ClientToolCall | PendingConfirmation>> {
    const names = [...tools];
    let state: SessionWait;
    try {
      state = await this.wait(sessionId);
    } catch (error) {
      if (!daemonHasNoWait(error)) throw error;
      this.noteOlderDaemon();
      return this.polledItems(sessionId, names);
    }
    return this.waitItems(sessionId, state, names);
  }

  /** The calls and confirmations a wait reported, for the tools you answer. */
  private waitItems(sessionId: string, state: SessionWait, tools: string[]): Array<ClientToolCall | PendingConfirmation> {
    const owner = { ...(state.endUser !== undefined ? { endUser: state.endUser } : {}), ...(state.labels ? { labels: state.labels } : {}) };
    const names = new Set(tools);
    return [...clientToolCalls(sessionId, state.pendingCalls, tools, owner), ...state.pendingConfirmations.filter((confirmation) => names.has(confirmation.tool))];
  }

  /** pendingCalls for daemons that predate waiting (see noteOlderDaemon). */
  private async polledItems(sessionId: string, tools: string[]): Promise<Array<ClientToolCall | PendingConfirmation>> {
    const res = await this.transport.request<{ permissions: PendingPermissionRecord[] | null; end_user?: string; labels?: Record<string, string> }>(
      `/v3/sessions/${encodeURIComponent(sessionId)}/permissions?status=pending&limit=100`
    );
    // The end user comes with the delivery, from the stored session.
    const items: Array<ClientToolCall | PendingConfirmation> = clientToolCalls(sessionId, res.data.permissions ?? [], tools, endUserFields(res.data as Record<string, unknown>));
    const names = new Set(tools);
    for (const confirmation of await this.confirmations(sessionId)) {
      if (names.has(confirmation.tool)) items.push(confirmation);
    }
    return items;
  }

  /**
   * Lists the agent's conversations, most recently updated first
   * (`GET /v3/sessions:agent`). A gateway or operator token lists its own
   * agent; the owner passes `agent`. Page with `nextCursor`.
   */
  async listSessions(params: ListAgentSessionsParams = {}): Promise<{ sessions: AgentSession[]; nextCursor?: string }> {
    const q = new URLSearchParams();
    if (params.agent) q.set('agent', toolName(params.agent));
    if (params.state) q.set('state', params.state);
    if (typeof params.updatedAfter === 'number') q.set('updated_after', String(params.updatedAfter));
    if (typeof params.updatedBefore === 'number') q.set('updated_before', String(params.updatedBefore));
    if (params.cursor) q.set('cursor', params.cursor);
    if (typeof params.limit === 'number') q.set('limit', String(params.limit));
    const res = await this.transport.request<{ sessions: AgentSession[] | null; next_cursor?: string }>(`/v3/sessions:agent${q.toString() ? `?${q.toString()}` : ''}`);
    return { sessions: res.data.sessions ?? [], ...(res.data.next_cursor ? { nextCursor: res.data.next_cursor } : {}) };
  }

  /**
   * Ends a conversation: it accepts no more messages, its run in flight and
   * pending tool calls stop. Ending an ended conversation changes nothing.
   */
  async endSession(sessionId: string, options: { reason?: string; clientRequestId?: string } = {}): Promise<{ ended: { at: number; by: string; reason?: string }; alreadyEnded: boolean; stoppedRunId?: string }> {
    const res = await this.transport.request<{ ended: { at: number; by: string; reason?: string }; already_ended?: boolean; stopped_run_id?: string }>(
      `/v3/sessions/${encodeURIComponent(sessionId)}/end`,
      { method: 'POST', body: { client_request_id: options.clientRequestId ?? requestId('sdk-end'), ...(options.reason ? { reason: options.reason } : {}) } },
    );
    return { ended: res.data.ended, alreadyEnded: !!res.data.already_ended, ...(res.data.stopped_run_id ? { stoppedRunId: res.data.stopped_run_id } : {}) };
  }

  /**
   * Hard deletes a conversation and its messages. The daemon refuses (409)
   * while a run or tool call is in flight: end the session first.
   */
  async deleteSession(sessionId: string): Promise<void> {
    await this.transport.request(`/v3/sessions/${encodeURIComponent(sessionId)}`, { method: 'DELETE' });
  }

  /** Reads the agent's event feed after a cursor (`GET /v3/sessions:agent-events`), oldest first. */
  async events(params: AgentEventsParams = {}): Promise<AgentEventPage> {
    const q = new URLSearchParams();
    if (params.agent) q.set('agent', toolName(params.agent));
    if (typeof params.after === 'number') q.set('after', String(params.after));
    if (typeof params.limit === 'number') q.set('limit', String(params.limit));
    if (params.kinds?.length) q.set('kind', params.kinds.join(','));
    const seconds = Math.max(0, Math.min(30, Math.floor(params.waitSeconds ?? 0)));
    if (seconds > 0) q.set('wait', String(seconds));
    const res = await this.transport.request<AgentEventPage>(`/v3/sessions:agent-events${q.toString() ? `?${q.toString()}` : ''}`, { signal: params.signal, timeoutMs: (seconds + 15) * 1000 });
    const { events, next_after, latest_seq, oldest_seq, gap } = res.data;
    return { events: events ?? [], next_after, latest_seq, oldest_seq, gap: !!gap };
  }

  /**
   * Follows the agent's event feed until `signal` aborts, waiting on the
   * daemon between entries (no polling). Yields a gap record when entries
   * after the cursor were already dropped by the feed's retention.
   */
  async *watchEvents(params: Omit<AgentEventsParams, 'waitSeconds' | 'limit'> = {}): AsyncGenerator<AgentEvent | AgentEventGap> {
    let after = params.after ?? 0;
    while (!params.signal?.aborted) {
      let page: AgentEventPage;
      try {
        page = await this.events({ ...params, after, waitSeconds: WAIT_SECONDS });
      } catch (error) {
        if (params.signal?.aborted) return;
        throw error;
      }
      if (page.gap) yield { kind: 'gap', from: after, to: page.next_after };
      for (const event of page.events) yield event;
      after = page.next_after;
    }
  }

  /** Confirmed tool calls in a session waiting for the person's approval (at most one). */
  async confirmations(sessionId: string): Promise<PendingConfirmation[]> {
    const res = await this.transport.request<{ confirmations: ClientToolConfirmationRecord[] | null; end_user?: string; labels?: Record<string, string> }>(
      `/v3/sessions/${encodeURIComponent(sessionId)}/confirmations`
    );
    const owner = endUserFields(res.data as Record<string, unknown>);
    return (res.data.confirmations ?? []).filter((record) => record.status === 'pending').map((record) => pendingConfirmation(sessionId, record, owner));
  }

  /**
   * Records the person's decision on a confirmation and returns its new state.
   * Approving needs the hash of the arguments the person was shown; the call
   * then waits for your handler like any other. `confirm` and `reject` do this
   * and also wait for the agent's reply.
   */
  async decideConfirmation(sessionId: string, confirmationId: string, decision: { decision: 'approve' | 'reject'; argumentsHash?: string; note?: string }): Promise<ClientToolConfirmationRecord> {
    if (typeof confirmationId !== 'string' || !confirmationId) throw new Error('confirmationId is required');
    if (decision.decision === 'approve' && !decision.argumentsHash) throw new Error('approving needs the argumentsHash the person was shown');
    const res = await this.transport.request<{ confirmation: ClientToolConfirmationRecord }>(
      `/v3/sessions/${encodeURIComponent(sessionId)}/confirmations/${encodeURIComponent(confirmationId)}`,
      {
        method: 'POST',
        body: {
          decision: decision.decision,
          ...(decision.argumentsHash ? { arguments_hash: decision.argumentsHash } : {}),
          ...(decision.note ? { note: decision.note } : {}),
        },
      }
    );
    return res.data.confirmation;
  }

  /**
   * The person approved: runs the confirmed call through `handlers` (with
   * exactly the confirmation's arguments) and returns the agent's reply. With
   * `endUser`, it refuses a session that belongs to anyone else before
   * recording the approval.
   */
  async confirm(sessionId: string, confirmation: PendingConfirmation, handlers: Record<string, ClientToolHandler>, options: TurnOptions = {}): Promise<ConverseResult> {
    return this.followTurn(sessionId, handlers, options, async () => {
      await this.decideConfirmation(sessionId, confirmation.id, { decision: 'approve', argumentsHash: confirmation.argumentsHash });
    });
  }

  /**
   * The person declined: the model is told "the person declined: <note>".
   * Returns the agent's reply. `endUser` works as on `confirm`.
   */
  async reject(sessionId: string, confirmation: PendingConfirmation, handlers: Record<string, ClientToolHandler>, options: TurnOptions & { note?: string } = {}): Promise<ConverseResult> {
    return this.followTurn(sessionId, handlers, options, async () => {
      await this.decideConfirmation(sessionId, confirmation.id, { decision: 'reject', note: options.note });
    });
  }

  /** Returns `result` (any JSON value) to the model as the tool's output. */
  async answer(sessionId: string, callId: string, result: unknown): Promise<void> {
    await this.resolve(sessionId, callId, { action: 'allow_once', reason: '', approved_arguments: { result: JSON.parse(JSON.stringify(result ?? null)) } });
  }

  /** Returns `reason` to the model as the tool's error. */
  async fail(sessionId: string, callId: string, reason: string): Promise<void> {
    await this.resolve(sessionId, callId, { action: 'deny', reason: String(reason || 'tool failed') });
  }

  /**
   * Answers one session's client tool calls with `handlers` until `signal`
   * aborts or the conversation has ended, waiting on the daemon between steps
   * (`intervalMs` applies only to daemons that predate waiting). Each call is
   * answered at most once; a handler error fails the call with "tool failed"
   * unless it is a ClientToolError. With `endUser`, a call whose session
   * belongs to anyone else is failed unanswered, and such a confirmation is
   * not passed to `onConfirm`. Confirmations are never answered here:
   * `onConfirm` is called once for each (show the approval card), and
   * `confirm` or `reject` records the person's decision.
   */
  async serve(sessionId: string, handlers: Record<string, ClientToolHandler>, options: ServeOptions = {}): Promise<void> {
    const handled = new Set<string>();
    const tools = Object.keys(handlers);
    let cursor: number | undefined;
    while (!options.signal?.aborted) {
      let state: SessionWait;
      try {
        state = await this.wait(sessionId, cursor === undefined ? { signal: options.signal } : { after: cursor, signal: options.signal });
      } catch (error) {
        if (options.signal?.aborted) return;
        if (cursor === undefined && daemonHasNoWait(error)) {
          this.noteOlderDaemon();
          return this.servePolling(sessionId, handlers, handled, options);
        }
        throw error;
      }
      cursor = state.cursor;
      await this.handleItems(sessionId, this.waitItems(sessionId, state, tools), handlers, handled, options);
      // An ended conversation starts no more runs, so no more calls come.
      if (state.ended && !state.run?.active) return;
    }
  }

  /** Answers each new call once and shows each new confirmation once. */
  private async handleItems(sessionId: string, items: Array<ClientToolCall | PendingConfirmation>, handlers: Record<string, ClientToolHandler>, handled: Set<string>, options: ServeOptions): Promise<void> {
    for (const item of items) {
      if (handled.has(item.id)) continue;
      handled.add(item.id);
      if (item.kind === 'confirmation') {
        if (options.endUser === undefined || item.endUser === options.endUser) await options.onConfirm?.(item);
        continue;
      }
      if (options.endUser !== undefined && item.endUser !== options.endUser) {
        await this.fail(sessionId, item.id, 'tool failed').catch(() => undefined);
        continue;
      }
      try {
        await this.answer(sessionId, item.id, await handlers[item.tool](item.arguments, item));
      } catch (error) {
        await this.fail(sessionId, item.id, error instanceof ClientToolError ? error.message : 'tool failed').catch(() => undefined);
      }
    }
  }

  /** serve for daemons that predate waiting (see noteOlderDaemon). */
  private async servePolling(sessionId: string, handlers: Record<string, ClientToolHandler>, handled: Set<string>, options: ServeOptions): Promise<void> {
    const interval = Math.max(50, options.intervalMs ?? 250);
    const tools = Object.keys(handlers);
    while (!options.signal?.aborted) {
      await this.handleItems(sessionId, await this.polledItems(sessionId, tools), handlers, handled, options);
      await sleep(interval, options.signal);
    }
  }

  /**
   * Sends one user message to a sealed agent's session, answers its client
   * tool calls with `handlers` while it runs, and returns once the run has
   * finished. This is the whole loop a gateway needs per visitor message.
   * It waits on the daemon between steps; `pollIntervalMs` applies only to
   * daemons that predate waiting. With `endUser`, it refuses a session that
   * belongs to anyone else before sending, and answers only that end user's
   * calls. When the agent asks the person to approve a confirmed tool call,
   * the result carries `confirmation`: pass it to `confirm` or `reject`.
   */
  async converse(sessionId: string, message: string, handlers: Record<string, ClientToolHandler>, options: TurnOptions = {}): Promise<ConverseResult> {
    const sessions = new SwarmSessionsNamespace(this.transport);
    return this.followTurn(sessionId, handlers, options, async () => {
      await sessions.sendMessage(sessionId, { content: message });
    });
  }

  /**
   * Starts a turn with `start`, answers client tool calls while it runs, and
   * returns once the agent has replied or has asked for a confirmation. With
   * `endUser`, it refuses a session that belongs to anyone else before
   * `start` runs.
   */
  private async followTurn(sessionId: string, handlers: Record<string, ClientToolHandler>, options: TurnOptions, start: () => Promise<void>): Promise<ConverseResult> {
    const sessions = new SwarmSessionsNamespace(this.transport);
    const initial = await sessions.get(sessionId);
    if (options.endUser !== undefined && initial.end_user !== options.endUser) {
      throw new Error('This conversation belongs to another end user');
    }
    let begin: SessionWait;
    try {
      begin = await this.wait(sessionId);
    } catch (error) {
      if (!daemonHasNoWait(error)) throw error;
      this.noteOlderDaemon();
      return this.followTurnPolling(sessionId, initial, handlers, options, start);
    }
    if (begin.ended) throw new Error('The conversation has ended; start a new session');
    const before = new Set((initial.messages ?? []).map((m: any) => m.id));
    const earlier = ((initial.raw ?? {}) as Record<string, any>).pending_confirmation?.id;
    const serving: ServeOptions = { onConfirm: options.onConfirm, ...(options.endUser !== undefined ? { endUser: options.endUser } : {}) };
    const tools = Object.keys(handlers);
    const deadline = Date.now() + (options.timeoutMs ?? 120_000);
    const handled = new Set<string>();
    await start();
    let cursor = begin.cursor;
    for (;;) {
      const remaining = deadline - Date.now();
      if (remaining <= 0) throw new Error('The agent did not finish in time; the run may still complete');
      const state = await this.wait(sessionId, { after: cursor, waitSeconds: Math.min(WAIT_SECONDS, Math.ceil(remaining / 1000)) });
      cursor = state.cursor;
      const items = this.waitItems(sessionId, state, tools);
      await this.handleItems(sessionId, items, handlers, handled, serving);
      const calls = items.filter((item) => item.kind === 'call');
      if (calls.length > 0 || state.run?.active || state.more) continue;
      const asked = items.find((item): item is PendingConfirmation => item.kind === 'confirmation' && item.id !== earlier);
      if (state.reasons.length === 0 && !state.ended && !asked) continue;
      const detail = await sessions.get(sessionId);
      const added = ((detail.messages ?? []) as Array<Record<string, any>>).filter((m) => !before.has(m.id));
      const failure = added.find((m) => m.role === 'system' && m.metadata?.message_kind === 'run_failure');
      if (failure) throw new Error(`The agent's run failed: ${String(failure.content ?? '').slice(0, 300)}`);
      const reply = added.filter((m) => m.role === 'assistant').map((m) => String(m.content ?? '')).join('\n\n');
      if (asked) return { reply, messages: added, confirmation: asked };
      if (added.some((m) => m.role === 'assistant')) return { reply, messages: added };
      if (state.ended) throw new Error('The conversation was ended before the agent replied');
    }
  }

  /** followTurn for daemons that predate waiting (see noteOlderDaemon). */
  private async followTurnPolling(sessionId: string, initial: { messages?: unknown[]; raw?: unknown }, handlers: Record<string, ClientToolHandler>, options: TurnOptions, start: () => Promise<void>): Promise<ConverseResult> {
    const sessions = new SwarmSessionsNamespace(this.transport);
    const before = new Set((initial.messages ?? []).map((m: any) => m.id));
    const earlier = ((initial.raw ?? {}) as Record<string, any>).pending_confirmation?.id;
    const controller = new AbortController();
    const serving = this.servePolling(sessionId, handlers, new Set<string>(), {
      signal: controller.signal,
      intervalMs: options.pollIntervalMs ?? 200,
      onConfirm: options.onConfirm,
      ...(options.endUser !== undefined ? { endUser: options.endUser } : {}),
    });
    const deadline = Date.now() + (options.timeoutMs ?? 120_000);
    try {
      await start();
      for (;;) {
        const detail = await sessions.get(sessionId);
        const raw = (detail.raw ?? {}) as Record<string, any>;
        const added = ((detail.messages ?? []) as Array<Record<string, any>>).filter((m) => !before.has(m.id));
        const pending = Array.isArray(raw.pending_permissions) ? raw.pending_permissions.length : 0;
        const failure = added.find((m) => m.role === 'system' && m.metadata?.message_kind === 'run_failure');
        const reply = added.filter((m) => m.role === 'assistant').map((m) => String(m.content ?? '')).join('\n\n');
        if (!raw.active_run_intent && failure) throw new Error(`The agent's run failed: ${String(failure.content ?? '').slice(0, 300)}`);
        const asked = raw.pending_confirmation as ClientToolConfirmationRecord | undefined;
        if (!raw.active_run_intent && asked?.id && asked.id !== earlier) {
          return { reply, messages: added, confirmation: pendingConfirmation(sessionId, asked, endUserFields(raw)) };
        }
        if (!raw.active_run_intent && pending === 0 && added.some((m) => m.role === 'assistant')) {
          return { reply, messages: added };
        }
        if (Date.now() > deadline) throw new Error('The agent did not finish in time; the run may still complete');
        await sleep(options.pollIntervalMs ?? 400);
      }
    } finally {
      controller.abort();
      await serving.catch(() => undefined);
    }
  }

  private async resolve(sessionId: string, callId: string, body: Record<string, unknown>): Promise<void> {
    if (typeof callId !== 'string' || !callId) throw new Error('callId is required');
    await this.transport.request(`/v3/sessions/${encodeURIComponent(sessionId)}/permissions/${encodeURIComponent(callId)}/resolve`, { method: 'POST', body });
  }
}
