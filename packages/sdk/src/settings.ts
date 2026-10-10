import type { SwarmTransport } from './transport.js';
import { SwarmNotFoundError } from './errors.js';

export interface ProviderStatus {
  id: string;
  ready: boolean;
  runnable: boolean;
  reason?: string;
  run_reason?: string;
  default_model?: string;
  default_thinking?: string;
  auth_methods?: { id: string; label: string; credential_type?: string; description?: string }[];
}

export interface ModelAssignment {
  provider: string;
  model: string;
  thinking: string;
  service_tier?: string;
  context_mode?: string;
}

export type SystemAgentSlot = 'compact' | 'finder' | 'coder' | 'designer' | 'router';

export interface AgentModelSettings {
  account_scope_id: string;
  updated_at: number;
  swarm: { action: ModelAssignment; plan: ModelAssignment };
  system_agents: Record<SystemAgentSlot, ModelAssignment>;
}

export interface AgentModelSettingsResponse {
  ok: boolean;
  agent_model_settings: AgentModelSettings;
  roles: Record<string, string>[];
}

export interface ModelRecommendation {
  role: string;
  thinking?: string;
  serving?: string;
  [key: string]: unknown;
}

/** Catalog extensions are preserved as unknown, never interpreted as runtime capability grants. */
export interface ModelCatalogEntry {
  provider: string;
  model: string;
  display_name?: string;
  provider_display_name?: string;
  catalog_id?: string;
  context_window: number;
  max_output_tokens: number;
  reasoning: boolean;
  thinking_options?: string[];
  default_thinking?: string;
  service_tiers?: string[];
  default_service_tier?: string;
  recommendations?: ModelRecommendation[];
  context_modes?: { mode: string; label?: string; context_window?: number; default?: boolean }[];
  source: string;
  fetched_at: number;
  expires_at: number;
  [extension: string]: unknown;
}

export interface ModelCatalogResponse {
  ok: boolean;
  provider: string;
  count: number;
  records: ModelCatalogEntry[];
  meta?: Record<string, unknown>;
  catalog_status: { configured: boolean; [key: string]: unknown };
}

export interface FleetModelAssignments {
  swarm: {
    action: ModelAssignment;
    plan: ModelAssignment;
  };
  system_agents: Record<SystemAgentSlot, ModelAssignment>;
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

  /**
   * Restores verified recommended defaults across all system agent and swarm core roles.
   * If expectedUpdatedAt is omitted, automatically reads the current setting's revision first.
   * An account with no model settings yet (a provider is connected but its
   * models were never set up) restores from revision 0: the daemon then sets
   * up the recommended models from a ready provider.
   */
  async restoreDefaults(expectedUpdatedAt?: number): Promise<AgentModelSettingsResponse> {
    let updatedAt = expectedUpdatedAt;
    if (typeof updatedAt !== 'number') {
      try {
        const current = await this.agentModels();
        updatedAt = current.agent_model_settings.updated_at;
      } catch (error) {
        if (!(error instanceof SwarmNotFoundError)) throw error;
        updatedAt = 0;
      }
    }
    return (
      await this.transport.request<AgentModelSettingsResponse>(
        '/v1/agent-model-settings/restore-defaults',
        {
          method: 'POST',
          body: { expected_updated_at: updatedAt },
        }
      )
    ).data;
  }

  /**
   * Resolves the recommended model and thinking level for each role from a provider's catalog.
   */
  async getRecommendationsForProvider(provider: string): Promise<FleetModelAssignments> {
    const normalizedProvider = provider.toLowerCase().trim();
    const catalog = await this.models(normalizedProvider);
    const records = catalog.records || [];

    const findForRole = (role: string): { model: string; thinking: string } | null => {
      for (const entry of records) {
        if (!entry.recommendations) continue;
        const rec = entry.recommendations.find(
          (r) => r.role?.toLowerCase().trim() === role.toLowerCase().trim()
        );
        if (rec) {
          return {
            model: entry.model,
            thinking: rec.thinking || entry.default_thinking || 'medium',
          };
        }
      }
      return null;
    };

    const autoMatch = findForRole('auto') || {
      model: records[0]?.model || 'default',
      thinking: records[0]?.default_thinking || 'high',
    };
    const planMatch = findForRole('plan') || autoMatch;
    const coderMatch = findForRole('coder') || autoMatch;
    const utilityMatch = findForRole('utility');
    const routerMatch = findForRole('router') || utilityMatch || autoMatch;
    const compactMatch = findForRole('compact') || utilityMatch || autoMatch;
    const finderMatch = findForRole('finder') || utilityMatch || autoMatch;
    const designerMatch = findForRole('designer') || planMatch || autoMatch;

    return {
      swarm: {
        action: { provider: normalizedProvider, model: autoMatch.model, thinking: autoMatch.thinking },
        plan: { provider: normalizedProvider, model: planMatch.model, thinking: planMatch.thinking },
      },
      system_agents: {
        coder: { provider: normalizedProvider, model: coderMatch.model, thinking: coderMatch.thinking },
        finder: { provider: normalizedProvider, model: finderMatch.model, thinking: finderMatch.thinking },
        designer: { provider: normalizedProvider, model: designerMatch.model, thinking: designerMatch.thinking },
        router: { provider: normalizedProvider, model: routerMatch.model, thinking: routerMatch.thinking },
        compact: { provider: normalizedProvider, model: compactMatch.model, thinking: compactMatch.thinking },
      },
    };
  }

  /**
   * Applies verified onboarding model recommendations for a specific provider across the entire fleet:
   * Swarm Action, Swarm Plan, and System Agents (Coder, Finder, Designer, Router, Compact).
   */
  async applyProviderFleet(provider: string): Promise<AgentModelSettingsResponse> {
    const fleet = await this.getRecommendationsForProvider(provider);
    return this.patch(fleet);
  }

  async patch(body: object): Promise<AgentModelSettingsResponse> {
    return (await this.transport.request<AgentModelSettingsResponse>('/v1/agent-model-settings', { method: 'PATCH', body })).data;
  }
}
