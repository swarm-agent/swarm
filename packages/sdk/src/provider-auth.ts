import type { SwarmTransport } from './transport.js';

export interface CredentialInput {
  provider: string; type: string; id?: string; label?: string; tags?: string[];
  api_key?: string; access_token?: string; refresh_token?: string; expires_at?: number;
  account_id?: string; active?: boolean;
}
export interface CredentialConnection { connected: boolean; method?: string; message?: string; verified_at?: number }
/** Redacted server status: never includes credential material. */
export interface CredentialStatus {
  id: string; provider: string; active: boolean; auth_type: string; label?: string; tags?: string[];
  updated_at: number; created_at: number; expires_at?: number; last4?: string;
  has_refresh_token?: boolean; has_account_id?: boolean; storage_mode: string;
  connection?: CredentialConnection;
  auto_defaults?: { applied: boolean; error?: string; provider?: string; model?: string; thinking?: string;
    global_model?: boolean; agents?: string[]; subagents?: string[];
    utility_provider?: string; utility_model?: string; utility_thinking?: string };
}
export interface CredentialList { provider?: string; query?: string; total: number; records: CredentialStatus[]; providers?: string[] }
export interface CodexLogin {
  session_id: string; provider: string; method: 'browser' | 'manual' | 'device'; label?: string;
  active: boolean; auth_url?: string; verification_url?: string; user_code?: string; expires_at?: number;
  status: string; error?: string; credential?: CredentialStatus;
}
export class SwarmCredentialsNamespace {
  constructor(private readonly transport: SwarmTransport) {}
  async list(options: { provider?: string; query?: string; limit?: number } = {}): Promise<CredentialList> {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(options)) if (value !== undefined) query.set(key, String(value));
    return (await this.transport.request<CredentialList>(`/v1/auth/credentials?${query}`)).data;
  }
  async save(input: CredentialInput): Promise<CredentialStatus> {
    return (await this.transport.request<CredentialStatus>('/v1/auth/credentials', { method: 'POST', body: input })).data;
  }
  async activate(provider: string, id: string): Promise<CredentialStatus> {
    return (await this.transport.request<CredentialStatus>('/v1/auth/credentials/active', { method: 'POST', body: { provider, id } })).data;
  }
  async verify(provider: string, id: string): Promise<{ provider: string; id: string; connection: CredentialConnection }> {
    return (await this.transport.request<{ provider: string; id: string; connection: CredentialConnection }>('/v1/auth/credentials/verify', { method: 'POST', body: { provider, id } })).data;
  }
  async delete(provider: string, id: string): Promise<{ ok: boolean; deleted: boolean; provider: string; id: string; cleanup: Record<string, unknown> }> {
    return (await this.transport.request<{ ok: boolean; deleted: boolean; provider: string; id: string; cleanup: Record<string, unknown> }>('/v1/auth/credentials/delete', { method: 'POST', body: { provider, id } })).data;
  }
}
export interface CodexDeviceCode {
  verification_url: string;
  user_code: string;
  session_id: string;
  expires_at?: number;
}

export interface CodexDeviceLoginOptions {
  label?: string;
  active?: boolean;
  intervalMs?: number;
  timeoutMs?: number;
  signal?: AbortSignal;
  onCode?: (code: CodexDeviceCode) => void;
  onStatus?: (status: CodexLogin) => void;
}

export class SwarmCodexAuthNamespace {
  constructor(private readonly transport: SwarmTransport) {}
  async start(input: { method: 'browser' | 'manual' | 'device'; label?: string; active?: boolean }): Promise<CodexLogin> {
    return (await this.transport.request<CodexLogin>('/v1/auth/codex/oauth/start', { method: 'POST', body: { ...input, provider: 'codex' } })).data;
  }
  async status(session_id: string): Promise<CodexLogin> {
    return (await this.transport.request<CodexLogin>(`/v1/auth/codex/oauth/status?${new URLSearchParams({ session_id })}`)).data;
  }
  async complete(session_id: string, callback_input: string): Promise<CodexLogin> {
    return (await this.transport.request<CodexLogin>('/v1/auth/codex/oauth/complete', { method: 'POST', body: { session_id, callback_input } })).data;
  }
  /**
   * High-level Codex device login flow.
   * Starts the OAuth device authorization session, invokes onCode with verification URL
   * and user code, and polls status until completion, timeout, or abort.
   */
  async loginDevice(options: CodexDeviceLoginOptions = {}): Promise<CodexLogin> {
    const startRes = await this.start({
      method: 'device',
      label: options.label ?? 'Codex Device Login',
      active: options.active ?? true,
    });
    if (options.onCode && startRes.verification_url && startRes.user_code) {
      options.onCode({
        verification_url: startRes.verification_url,
        user_code: startRes.user_code,
        session_id: startRes.session_id,
        expires_at: startRes.expires_at,
      });
    }
    const intervalMs = Math.max(500, options.intervalMs ?? 2000);
    const timeoutMs = options.timeoutMs ?? 15 * 60 * 1000;
    const deadline = Date.now() + timeoutMs;

    while (Date.now() < deadline) {
      if (options.signal?.aborted) {
        throw new Error('Codex device login aborted by caller');
      }
      await new Promise<void>((resolve) => setTimeout(resolve, intervalMs));
      if (options.signal?.aborted) {
        throw new Error('Codex device login aborted by caller');
      }
      const current = await this.status(startRes.session_id);
      options.onStatus?.(current);
      if (current.status === 'success') {
        return current;
      }
      if (current.status === 'error' || current.status === 'failed' || current.status === 'expired') {
        throw new Error(current.error || `Codex login failed with status: ${current.status}`);
      }
    }
    throw new Error(`Codex device login timed out after ${timeoutMs}ms`);
  }
  /**
   * Checks whether Codex is already connected and active.
   */
  async isAuthenticated(): Promise<boolean> {
    try {
      const res = await (await this.transport.request<CredentialList>('/v1/auth/credentials?provider=codex')).data;
      if (!res?.records?.length) return false;
      return res.records.some((r) => r.provider === 'codex' && r.active && r.connection?.connected !== false);
    } catch {
      return false;
    }
  }
  /**
   * Retrieves the active Codex credential status if one exists.
   */
  async getCredential(): Promise<CredentialStatus | null> {
    try {
      const res = await (await this.transport.request<CredentialList>('/v1/auth/credentials?provider=codex')).data;
      const match = res?.records?.find((r) => r.provider === 'codex' && r.active);
      return match ?? null;
    } catch {
      return null;
    }
  }
}
export interface OnboardingStatus {
  ok: boolean; needs_onboarding: boolean;
  identity: { bootstrapped: boolean; user_id?: string; account_scope_id?: string; username?: string; team_id?: string; team_display_name?: string; team_default?: boolean; membership_role?: string };
  heuristics: { missing_swarm_name: boolean; credential_count: number; agent_count: number; saved_workspace_count: number; vault_configured: boolean };
  config: { swarm_name: string; desktop_onboarding_complete: boolean; mode: string; port: number; desktop_port: number; advertise_port: number; peer_transport_port: number; [key: string]: unknown };
  workspace_guidance?: Record<string, unknown>; session?: { expires_at?: string }; tailscale: Record<string, unknown>;
}
export interface OnboardingUpdate {
  username?: string; swarm_name?: string; desktop_onboarding_complete?: boolean; mode?: string;
  port?: number; advertise_host?: string; advertise_port?: number; tailscale_url?: string; peer_transport_port?: number;
}
export class SwarmOnboardingNamespace {
  constructor(private readonly transport: SwarmTransport) {}
  async get(): Promise<OnboardingStatus> { return (await this.transport.request<OnboardingStatus>('/v1/onboarding')).data; }
  async update(body: OnboardingUpdate): Promise<OnboardingStatus> {
    return (await this.transport.request<OnboardingStatus>('/v1/onboarding', { method: 'POST', body })).data;
  }
  /**
   * Ensures the headless daemon identity is bootstrapped.
   * If the daemon requires onboarding, it automatically sets default identity parameters
   * so provider authentication and sessions can immediately proceed without manual wizard steps.
   */
  async ensureBootstrapped(options: { username?: string; swarm_name?: string } = {}): Promise<OnboardingStatus> {
    const status = await this.get();
    if (!status.needs_onboarding && status.identity?.bootstrapped) {
      return status;
    }
    if (status.identity?.bootstrapped) {
      return this.update({ desktop_onboarding_complete: true });
    }
    return this.update({
      username: options.username ?? 'owner',
      swarm_name: options.swarm_name ?? 'Headless Swarm',
      desktop_onboarding_complete: true,
    });
  }
  /** First credential only. Use auth.credentials.save for subsequent credentials. */
  async credential(body: Omit<CredentialInput, 'id'>): Promise<CredentialStatus> {
    return (await this.transport.request<CredentialStatus>('/v1/onboarding/provider/credential', { method: 'POST', body })).data;
  }
}
