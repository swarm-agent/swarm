import type { PendingPermissionRecord } from './permissions.js';
import type { SwarmTransport } from './transport.js';

/** A typed tool the application answers. Swarm validates and waits; it never executes it. */
export interface ClientToolDefinition {
  name: string;
  description: string;
  /** JSON Schema for the arguments; must be `type: "object"` with only local `$ref`s. */
  input_schema: Record<string, unknown>;
  /** `write` marks a tool with external effects (confirm with a person before running it). */
  effect?: 'read' | 'write';
  /** How long a call waits for an answer before the model gets "client tool timed out" (max 300000). */
  timeout_ms?: number;
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
}

/** A pending client tool call your application must answer. */
export interface ClientToolCall {
  /** Pass to `answer`/`fail`. */
  id: string;
  sessionId: string;
  runId: string;
  tool: string;
  /** Already validated by Swarm against the tool's input_schema. */
  arguments: Record<string, unknown>;
  createdAt: number;
}

/** Throw from a handler to send this message to the model; other errors send "tool failed". */
export class ClientToolError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ClientToolError';
  }
}

export type ClientToolHandler = (args: Record<string, unknown>, call: ClientToolCall) => Promise<unknown> | unknown;

const NAME = /^[a-z][a-z0-9_]{0,63}$/;

function toolName(name: string): string {
  if (typeof name !== 'string' || !NAME.test(name)) throw new Error('tool and agent names are lowercase letters, digits and underscores');
  return name;
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
        tool_contract: { preset: 'custom', tools },
      },
    });
    return res.data.profile;
  }

  /**
   * Mints the token a gateway holds to serve one sealed agent. It can only
   * create and use that agent's sessions and answer its client tool calls;
   * it stops working if the agent is given any built-in tool. Owner only.
   */
  async createGatewayToken(agentName: string, options: { name?: string; expiresInSeconds?: number } = {}): Promise<{ token: string; record: Record<string, unknown> }> {
    const agent = toolName(agentName);
    const res = await this.transport.request<{ token: string; record: Record<string, unknown> }>('/v3/auth/tokens', {
      method: 'POST',
      body: { name: options.name ?? `${agent} gateway`, agent_name: agent, ...(options.expiresInSeconds ? { expires_in_seconds: options.expiresInSeconds } : {}) },
    });
    return { token: res.data.token, record: res.data.record };
  }

  async removeAgent(name: string): Promise<void> {
    await this.transport.request(`/v2/agents/${toolName(name)}`, { method: 'DELETE' });
  }

  async removeClientTool(name: string): Promise<void> {
    await this.transport.request(`/v2/custom-tools/${toolName(name)}`, { method: 'DELETE' });
  }

  /** Client tool calls waiting in a session, limited to the tool names you answer. */
  async pendingCalls(sessionId: string, tools: Iterable<string>): Promise<ClientToolCall[]> {
    const names = new Set(tools);
    const res = await this.transport.request<{ permissions: PendingPermissionRecord[] | null }>(
      `/v3/sessions/${encodeURIComponent(sessionId)}/permissions?status=pending&limit=100`
    );
    const calls: ClientToolCall[] = [];
    for (const record of res.data.permissions ?? []) {
      if (!names.has(record.tool_name) || record.session_id !== sessionId) continue;
      let args: unknown;
      try { args = JSON.parse(record.tool_call_arguments || record.tool_arguments || '{}'); } catch { args = null; }
      if (!args || typeof args !== 'object' || Array.isArray(args)) continue;
      calls.push({ id: record.id, sessionId, runId: record.run_id, tool: record.tool_name, arguments: args as Record<string, unknown>, createdAt: record.created_at });
    }
    return calls;
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
   * call with "tool failed" unless it is a ClientToolError.
   */
  async serve(sessionId: string, handlers: Record<string, ClientToolHandler>, options: { signal?: AbortSignal; intervalMs?: number } = {}): Promise<void> {
    const handled = new Set<string>();
    const interval = Math.max(50, options.intervalMs ?? 250);
    while (!options.signal?.aborted) {
      for (const call of await this.pendingCalls(sessionId, Object.keys(handlers))) {
        if (handled.has(call.id)) continue;
        handled.add(call.id);
        try {
          await this.answer(sessionId, call.id, await handlers[call.tool](call.arguments, call));
        } catch (error) {
          await this.fail(sessionId, call.id, error instanceof ClientToolError ? error.message : 'tool failed').catch(() => undefined);
        }
      }
      await new Promise<void>((resolve) => {
        const timer = setTimeout(resolve, interval);
        options.signal?.addEventListener('abort', () => { clearTimeout(timer); resolve(); }, { once: true });
      });
    }
  }

  private async resolve(sessionId: string, callId: string, body: Record<string, unknown>): Promise<void> {
    if (typeof callId !== 'string' || !callId) throw new Error('callId is required');
    await this.transport.request(`/v3/sessions/${encodeURIComponent(sessionId)}/permissions/${encodeURIComponent(callId)}/resolve`, { method: 'POST', body });
  }
}
