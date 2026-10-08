import type { SwarmTransport } from './transport.js';
import { confirmPermissionResolution, permissionId as validatePermissionId, permissionResolutionBody } from './permission-ui.js';
import type { PermissionAction, ResolvePermissionOptions } from './permission-ui.js';
export * from './permission-ui.js';

export interface PermissionPolicy {
  version: number;
  bash_profile: string;
  rules?: PermissionRule[];
  updated_at?: number;
}

export interface PermissionRule {
  id: string;
  kind: string;
  decision: string;
  tool?: string;
  pattern?: string;
  created_at?: number;
  updated_at?: number;
}

export interface PermissionRuleInput {
  kind: string;
  decision: 'allow' | 'deny';
  tool?: string;
  pattern?: string;
}

export interface PendingPermissionRecord {
  id: string;
  session_id: string;
  run_id: string;
  step?: number;
  call_id?: string;
  tool_name: string;
  tool_arguments?: string;
  tool_call_arguments?: string;
  approved_arguments?: string;
  requirement: string;
  mode: string;
  status: 'pending' | 'approved' | 'denied' | string;
  decision?: string;
  reason?: string;
  execution_status?: string;
  permission_requested_at?: number;
  resolved_at?: number;
  created_at: number;
  updated_at: number;
}

export interface ResolvePermissionResult {
  ok: boolean;
  session_id: string;
  permission: PendingPermissionRecord;
  saved_rule?: PermissionRule | null;
}

export interface ResolveAllPermissionsResult {
  ok: boolean;
  session_id: string;
  count: number;
  resolved: PendingPermissionRecord[];
}

export interface PermissionExplainResult {
  decision: string;
  source: string;
  reason: string;
  tool_name?: string;
  command?: string;
  rule_preview?: string;
  bash_profile?: string;
  profile_decision?: string;
  profile_reason?: string;
}

export class SwarmPermissionsNamespace {
  constructor(private readonly transport: SwarmTransport) {}
  private readonly resolving = new Set<string>();

  /**
   * Retrieves the current account-wide permission policy, including bash profile and persistent rules.
   */
  async getPolicy(): Promise<PermissionPolicy> {
    const res = await this.transport.request<{ ok: boolean; policy: PermissionPolicy }>('/v1/permissions');
    return res.data.policy;
  }

  /**
   * Whether permissionless mode (bypass permissions) is on.
   */
  async bypass(): Promise<boolean> {
    const res = await this.transport.request<{ ok: boolean; bypass_permissions?: boolean }>('/v1/permissions');
    return res.data.bypass_permissions === true;
  }

  /**
   * Toggles permissionless mode (bypass permissions).
   * When enabled, tool executions run autonomously without pausing for manual approvals.
   */
  async setBypass(enabled: boolean): Promise<boolean> {
    const res = await this.transport.request<{ ok: boolean; bypass_permissions: boolean }>(
      '/v1/permissions/bypass',
      { method: 'POST', body: { enabled } }
    );
    return res.data.bypass_permissions === true;
  }

  /**
   * Updates the bash approval profile ('strict' | 'onboarding' | 'permissive').
   */
  async setBashProfile(profile: 'strict' | 'onboarding' | 'permissive'): Promise<string> {
    const res = await this.transport.request<{ ok: boolean; bash_profile: string }>(
      '/v1/permissions/bash-profile',
      { method: 'POST', body: { bash_profile: profile.trim() } }
    );
    return res.data.bash_profile;
  }

  /**
   * Adds a persistent permission rule (e.g. allow tool 'search' or allow specific command patterns).
   */
  async addRule(rule: PermissionRuleInput): Promise<PermissionRule> {
    const res = await this.transport.request<{ ok: boolean; rule: PermissionRule }>(
      '/v1/permissions',
      { method: 'POST', body: rule }
    );
    return res.data.rule;
  }

  /**
   * Removes a persistent permission rule by ID.
   */
  async removeRule(ruleId: string): Promise<boolean> {
    const res = await this.transport.request<{ ok?: boolean; removed: boolean }>(
      `/v1/permissions/${encodeURIComponent(ruleId.trim())}`,
      { method: 'DELETE' }
    );
    return res.data.removed === true;
  }

  /**
   * Resets the permission policy to default system state.
   */
  async resetPolicy(): Promise<PermissionPolicy> {
    const res = await this.transport.request<{ ok: boolean; policy: PermissionPolicy }>(
      '/v1/permissions/reset',
      { method: 'POST', body: {} }
    );
    return res.data.policy;
  }

  /**
   * Explains how the permission policy evaluates a given mode, tool, and arguments.
   */
  async explain(mode: string, tool: string, args: string = ''): Promise<PermissionExplainResult> {
    const query = new URLSearchParams({ mode, tool, arguments: args });
    const res = await this.transport.request<{ ok: boolean; explain: PermissionExplainResult }>(
      `/v1/permissions/explain?${query}`
    );
    return res.data.explain;
  }

  /**
   * Lists pending permissions for a specific session that are blocking tool execution.
   */
  async listSessionPending(sessionId: string, limit = 50): Promise<PendingPermissionRecord[]> {
    sessionId = validatePermissionId(sessionId, 'sessionId');
    if (!Number.isInteger(limit) || limit < 1 || limit > 1000) throw new Error('limit must be an integer from 1 to 1000');
    const encId = encodeURIComponent(sessionId);
    const res = await this.transport.request<{ ok: boolean; count: number; permissions: PendingPermissionRecord[] }>(
      `/v3/sessions/${encId}/permissions?status=pending&limit=${limit}`
    );
    if (res.data?.ok === true && res.data.count === 0 && res.data.permissions === null) return [];
    if (res.data?.ok !== true || !Array.isArray(res.data.permissions) || res.data.permissions.some(p => !p || p.session_id !== sessionId || typeof p.id !== 'string' || !p.id)) {
      throw new Error('Invalid permission list response; refresh the conversation');
    }
    return res.data.permissions;
  }

  /**
   * Lists all permissions (pending and resolved) for a session.
   */
  async listSessionAll(sessionId: string, limit = 100): Promise<PendingPermissionRecord[]> {
    sessionId = validatePermissionId(sessionId, 'sessionId');
    if (!Number.isInteger(limit) || limit < 1 || limit > 1000) throw new Error('limit must be an integer from 1 to 1000');
    const encId = encodeURIComponent(sessionId);
    const res = await this.transport.request<{ ok: boolean; count: number; permissions: PendingPermissionRecord[] }>(
      `/v3/sessions/${encId}/permissions?status=all&limit=${limit}`
    );
    if (res.data?.ok === true && res.data.count === 0 && res.data.permissions === null) return [];
    if (res.data?.ok !== true || !Array.isArray(res.data.permissions) || res.data.permissions.some(p => !p || p.session_id !== sessionId || typeof p.id !== 'string' || !p.id)) {
      throw new Error('Invalid permission list response; refresh the conversation');
    }
    return res.data.permissions;
  }

  /**
   * Resolves a single pending permission on a session.
   * Actions:
   * - 'allow_once': Approve this exact tool invocation once.
   * - 'deny' / 'deny_once': Deny this exact tool invocation once.
   * - 'allow_always': Approve this tool call and create a persistent allow rule.
   * - 'deny_always': Deny this tool call and create a persistent deny rule.
   */
  async resolve(
    sessionId: string,
    permissionId: string,
    action: PermissionAction,
    options: ResolvePermissionOptions = {}
  ): Promise<ResolvePermissionResult> {
    const session = validatePermissionId(sessionId, 'sessionId');
    const id = validatePermissionId(permissionId);
    const body = permissionResolutionBody(action, options);
    const key = JSON.stringify([session, id]);
    if (this.resolving.has(key)) throw new Error('Permission resolution already in flight');
    this.resolving.add(key);
    try {
      const res = await this.transport.request<ResolvePermissionResult>(
        `/v3/sessions/${encodeURIComponent(session)}/permissions/${encodeURIComponent(id)}/resolve`,
        { method: 'POST', body }
      );
      return confirmPermissionResolution(res.data, session, id, action, options);
    } finally { this.resolving.delete(key); }
  }

  /**
   * Resolves all pending permissions on a session in a single call.
   */
  async resolveAll(
    sessionId: string,
    action: 'allow_once' | 'deny',
    options: { reason?: string; limit?: number } = {}
  ): Promise<ResolveAllPermissionsResult> {
    const encSession = encodeURIComponent(sessionId.trim());
    const res = await this.transport.request<ResolveAllPermissionsResult>(
      `/v3/sessions/${encSession}/permissions/resolve_all`,
      {
        method: 'POST',
        body: {
          action,
          reason: options.reason ?? '',
          limit: options.limit ?? 50,
        },
      }
    );
    return res.data;
  }
}
