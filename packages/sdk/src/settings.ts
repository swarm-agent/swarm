import type { SwarmTransport } from './transport.js';

export interface ProviderStatus {
  id: string; ready: boolean; runnable: boolean; reason?: string; run_reason?: string;
  default_model?: string; default_thinking?: string;
  auth_methods?: { id: string; label: string; credential_type?: string; description?: string }[];
}
export interface ModelAssignment {
  provider: string; model: string; thinking: string; service_tier?: string; context_mode?: string;
}
export type SystemAgentSlot = 'compact' | 'finder' | 'coder' | 'designer' | 'router';
export interface AgentModelSettings {
  account_scope_id: string; updated_at: number;
  swarm: { action: ModelAssignment; plan: ModelAssignment };
  system_agents: Record<SystemAgentSlot, ModelAssignment>;
}
export interface AgentModelSettingsResponse {
  ok: boolean; agent_model_settings: AgentModelSettings;
  roles: Record<string, string>[];
}
/** Catalog extensions are preserved as unknown, never interpreted as runtime capability grants. */
export interface ModelCatalogEntry {
  provider: string; model: string; display_name?: string; provider_display_name?: string;
  catalog_id?: string; context_window: number; max_output_tokens: number; reasoning: boolean;
  thinking_options?: string[]; default_thinking?: string; service_tiers?: string[];
  default_service_tier?: string;
  context_modes?: { mode: string; label?: string; context_window?: number; default?: boolean }[];
  source: string; fetched_at: number; expires_at: number;
  [extension: string]: unknown;
}
export interface ModelCatalogResponse {
  ok: boolean; provider: string; count: number; records: ModelCatalogEntry[];
  meta?: Record<string, unknown>; catalog_status: { configured: boolean; [key: string]: unknown };
}

/** Account-scoped settings. Requires the local administrative connection, not a session token. */
export class SwarmSettingsNamespace {
  constructor(private readonly transport: SwarmTransport) {}
  async providers(): Promise<ProviderStatus[]> {
    return (await this.transport.request<{ providers: ProviderStatus[] }>('/v1/providers')).data.providers;
  }
  async models(provider: string, limit = 500): Promise<ModelCatalogResponse> {
    const query = new URLSearchParams({ provider, limit: String(limit) });
    return (await this.transport.request<ModelCatalogResponse>(`/v1/model/catalog?${query}`)).data;
  }
  async agentModels(): Promise<AgentModelSettingsResponse> {
    return (await this.transport.request<AgentModelSettingsResponse>('/v1/agent-model-settings')).data;
  }
  async setSwarmModel(slot: 'action' | 'plan', assignment: ModelAssignment): Promise<AgentModelSettingsResponse> {
    return this.patch({ swarm: { [slot]: assignment } });
  }
  async setSystemAgentModel(slot: SystemAgentSlot, assignment: ModelAssignment): Promise<AgentModelSettingsResponse> {
    return this.patch({ system_agents: { [slot]: assignment } });
  }
  private async patch(body: object): Promise<AgentModelSettingsResponse> {
    return (await this.transport.request<AgentModelSettingsResponse>('/v1/agent-model-settings', { method: 'PATCH', body })).data;
  }
}
