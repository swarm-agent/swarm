import type { PendingPermissionRecord } from './permissions.js';
import { SwarmSessionsNamespace } from './sessions.js';
import type { SwarmTransport } from './transport.js';

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
  /** Bounds one message's cost. Unset fields use the sealed defaults (4 model calls, 1024 output tokens, last 40 messages, 60 s). */
  limits?: { max_steps?: number; max_output_tokens?: number; max_history_messages?: number; run_timeout_ms?: number };
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

const NAME = /^[a-z][a-z0-9_]{0,63}$/;

function toolName(name: string): string {
  if (typeof name !== 'string' || !NAME.test(name)) throw new Error('tool and agent names are lowercase letters, digits and underscores');
  return name;
}

/** Who holds a conversation, as stored in its metadata under `takeover`. */
export type Takeover = { state: 'human'; by: string; since: number } | { state: 'agent'; ended_at: number };

function operatorRequestId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return `sdk-operator-${crypto.randomUUID()}`;
  return `sdk-operator-${Date.now().toString(36)}-${Math.random().toString(36).substring(2, 10)}`;
}

/**
 * Sealed agents and client tools (`/v2/agents`, `/v2/custom-tools`). Defining
 * them needs the owner or an `admin` token: keep this in a trusted backend.
 */
export class SwarmAgentsNamespace {
  constructor(private readonly transport: SwarmTransport) {}

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
   * What waits on your application in a session, limited to the tool names you
   * answer: client tool calls to run (`kind: 'call'`) and confirmed calls
   * waiting for the person's approval (`kind: 'confirmation'`).
   */
  async pendingCalls(sessionId: string, tools: Iterable<string>): Promise<Array<ClientToolCall | PendingConfirmation>> {
    const names = new Set(tools);
    const res = await this.transport.request<{ permissions: PendingPermissionRecord[] | null; end_user?: string; labels?: Record<string, string> }>(
      `/v3/sessions/${encodeURIComponent(sessionId)}/permissions?status=pending&limit=100`
    );
    // The end user comes with the delivery, from the stored session.
    const owner = endUserFields(res.data as Record<string, unknown>);
    const calls: Array<ClientToolCall | PendingConfirmation> = [];
    for (const record of res.data.permissions ?? []) {
      if (!names.has(record.tool_name) || record.session_id !== sessionId) continue;
      let args: unknown;
      try { args = JSON.parse(record.tool_call_arguments || record.tool_arguments || '{}'); } catch { args = null; }
      if (!args || typeof args !== 'object' || Array.isArray(args)) continue;
      calls.push({ kind: 'call', id: record.id, sessionId, runId: record.run_id, tool: record.tool_name, arguments: args as Record<string, unknown>, createdAt: record.created_at, ...owner });
    }
    for (const confirmation of await this.confirmations(sessionId)) {
      if (names.has(confirmation.tool)) calls.push(confirmation);
    }
    return calls;
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
  async confirm(sessionId: string, confirmation: PendingConfirmation, handlers: Record<string, ClientToolHandler>, options: { timeoutMs?: number; pollIntervalMs?: number; endUser?: string; onConfirm?: ConfirmationHandler } = {}): Promise<ConverseResult> {
    return this.followTurn(sessionId, handlers, options, async () => {
      await this.decideConfirmation(sessionId, confirmation.id, { decision: 'approve', argumentsHash: confirmation.argumentsHash });
    });
  }

  /**
   * The person declined: the model is told "the person declined: <note>".
   * Returns the agent's reply. `endUser` works as on `confirm`.
   */
  async reject(sessionId: string, confirmation: PendingConfirmation, handlers: Record<string, ClientToolHandler>, options: { note?: string; timeoutMs?: number; pollIntervalMs?: number; endUser?: string; onConfirm?: ConfirmationHandler } = {}): Promise<ConverseResult> {
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
   * aborts. Each call is answered at most once; a handler error fails the
   * call with "tool failed" unless it is a ClientToolError. With `endUser`,
   * a call whose session belongs to anyone else is failed unanswered, and
   * such a confirmation is not passed to `onConfirm`.
   * Confirmations are never answered here: `onConfirm` is called once for
   * each (show the approval card), and `confirm` or `reject` records the
   * person's decision.
   */
  async serve(sessionId: string, handlers: Record<string, ClientToolHandler>, options: { signal?: AbortSignal; intervalMs?: number; endUser?: string; onConfirm?: ConfirmationHandler } = {}): Promise<void> {
    const handled = new Set<string>();
    const interval = Math.max(50, options.intervalMs ?? 250);
    while (!options.signal?.aborted) {
      for (const item of await this.pendingCalls(sessionId, Object.keys(handlers))) {
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
      await new Promise<void>((resolve) => {
        const timer = setTimeout(resolve, interval);
        options.signal?.addEventListener('abort', () => { clearTimeout(timer); resolve(); }, { once: true });
      });
    }
  }

  /**
   * Sends one user message to a sealed agent's session, answers its client
   * tool calls with `handlers` while it runs, and returns once the run has
   * finished. This is the whole loop a gateway needs per visitor message.
   * With `endUser`, it refuses a session that belongs to anyone else before
   * sending, and answers only that end user's calls.
   * When the agent asks the person to approve a confirmed tool call, the
   * result carries `confirmation`: pass it to `confirm` or `reject`.
   */
  async converse(sessionId: string, message: string, handlers: Record<string, ClientToolHandler>, options: { timeoutMs?: number; pollIntervalMs?: number; endUser?: string; onConfirm?: ConfirmationHandler } = {}): Promise<ConverseResult> {
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
  private async followTurn(sessionId: string, handlers: Record<string, ClientToolHandler>, options: { timeoutMs?: number; pollIntervalMs?: number; endUser?: string; onConfirm?: ConfirmationHandler }, start: () => Promise<void>): Promise<ConverseResult> {
    const sessions = new SwarmSessionsNamespace(this.transport);
    const initial = await sessions.get(sessionId);
    if (options.endUser !== undefined && initial.end_user !== options.endUser) {
      throw new Error('This conversation belongs to another end user');
    }
    const before = new Set((initial.messages ?? []).map((m: any) => m.id));
    const earlier = ((initial.raw ?? {}) as Record<string, any>).pending_confirmation?.id;
    const controller = new AbortController();
    const serving = this.serve(sessionId, handlers, {
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
        await new Promise((resolve) => setTimeout(resolve, options.pollIntervalMs ?? 400));
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
