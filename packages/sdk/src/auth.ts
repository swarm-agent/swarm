import { SwarmCredentialsNamespace, SwarmCodexAuthNamespace } from './provider-auth.js';
import { SwarmSettingsNamespace } from './settings.js';
import type { SwarmTransport } from './transport.js';
import type {
  CreateScopedTokenParams,
  CreateScopedTokenResult,
  DesktopSessionBootstrap,
  ScopedTokenRecord,
} from './types.js';

export class SwarmAuthNamespace {
  readonly credentials: SwarmCredentialsNamespace;
  readonly codex: SwarmCodexAuthNamespace;
  private transport: SwarmTransport;
  private onTokenUpdate?: (token: string) => void;

  constructor(transport: SwarmTransport, onTokenUpdate?: (token: string) => void) {
    this.transport = transport;
    this.credentials = new SwarmCredentialsNamespace(transport);
    this.codex = new SwarmCodexAuthNamespace(transport);
    this.onTokenUpdate = onTokenUpdate;
  }

  /**
   * Bootstraps local desktop session credentials.
   * Useful in local development or testbench environments to obtain administrative tokens.
   * By default, automatically updates the client's bearer token with the returned token.
   */
  async bootstrapDesktopSession(options: {
    origin?: string;
    updateClientToken?: boolean;
  } = {}): Promise<DesktopSessionBootstrap> {
    const baseUrl = this.transport.getConfig().baseUrl.replace(/\/+$/, '');
    const origin = options.origin ?? baseUrl;

    const res = await this.transport.request<DesktopSessionBootstrap>('/v1/auth/desktop/session', {
      method: 'GET',
      headers: {
        'Origin': origin,
        'Referer': `${origin}/app`,
        'Sec-Fetch-Site': 'same-origin',
      },
    });

    const data = res.data;
    if (options.updateClientToken !== false && data?.token) {
      this.transport.setConfig({ token: data.token });
      if (this.onTokenUpdate) {
        this.onTokenUpdate(data.token);
      }
    }

    return data;
  }

  /**
   * Creates a new scoped deploy token with granular permission scopes.
   * Returns the plaintext token (prefixed with 'swk_') along with the token record.
   * Store the token securely—it cannot be retrieved again in plaintext!
   */
  async createScopedToken(params: CreateScopedTokenParams): Promise<CreateScopedTokenResult> {
    const res = await this.transport.request<CreateScopedTokenResult>('/v3/auth/tokens', {
      method: 'POST',
      body: {
        name: params.name,
        scopes: params.scopes,
        worker_id: params.worker_id,
        worker_name: params.worker_name,
        expires_in_seconds: params.expires_in_seconds,
      },
    });
    return res.data;
  }

  /**
   * Creates an AI key for Swarm Control (`/mcp` on the scoped-token gateway).
   * `read` keys see and call only read tools; `write` keys may also start and
   * steer sessions; neither may approve tool calls or manage anything. `full`
   * keys are for an AI the owner trusts to run the box: they also approve
   * tool calls and manage models, workers, limits, custom agents, client app
   * keys and ChatGPT sign-in. Every level reaches only `/mcp`. Lifetime
   * defaults to 30 days (at most 365). Owner only.
   */
  async createAIKey(params: { name: string; access: 'read' | 'write' | 'full'; expires_in_seconds?: number }): Promise<CreateScopedTokenResult> {
    const res = await this.transport.request<CreateScopedTokenResult>('/v3/auth/tokens', {
      method: 'POST',
      body: { name: params.name, ai_access: params.access, expires_in_seconds: params.expires_in_seconds },
    });
    return res.data;
  }

  /**
   * Lists all scoped deploy tokens for the active account.
   * Secret hashes are masked; hints (e.g. 'swk_...abcd') are displayed.
   */
  async listScopedTokens(): Promise<ScopedTokenRecord[]> {
    const res = await this.transport.request<{ ok: boolean; tokens: ScopedTokenRecord[] }>('/v3/auth/tokens', {
      method: 'GET',
    });
    return res.data?.tokens ?? [];
  }

  /**
   * Revokes a scoped token by ID.
   * The token immediately ceases to authenticate requests.
   */
  async revokeScopedToken(tokenId: string): Promise<ScopedTokenRecord> {
    const id = encodeURIComponent(tokenId.trim());
    const res = await this.transport.request<{ ok: boolean; record: ScopedTokenRecord }>(`/v3/auth/tokens/${id}/revoke`, {
      method: 'POST',
    });
    return res.data.record;
  }

  /**
   * Deletes or purges a scoped token.
   * If purge is true, the token record and index are permanently deleted from store.
   */
  async deleteScopedToken(tokenId: string, options: { purge?: boolean } = {}): Promise<{ ok: boolean; deleted: boolean }> {
    const id = encodeURIComponent(tokenId.trim());
    const query = options.purge ? '?purge=true' : '';
    const res = await this.transport.request<{ ok: boolean; deleted: boolean }>(`/v3/auth/tokens/${id}${query}`, {
      method: 'DELETE',
    });
    return res.data;
  }

  /**
   * Automatically configures provider credentials and the AI fleet from environment variables or existing credentials.
   * Checks for:
   * - OPENAI_API_KEY -> provider 'openai'
   * - ANTHROPIC_API_KEY -> provider 'anthropic'
   * - GEMINI_API_KEY / GOOGLE_API_KEY -> provider 'google'
   * - DEEPSEEK_API_KEY -> provider 'deepseek'
   * - GROQ_API_KEY -> provider 'groq'
   * - MISTRAL_API_KEY -> provider 'mistral'
   * Also verifies if Codex is authenticated via OAuth.
   * When credentials are found or active, automatically configures the whole fleet (Swarm Core & System Agents)
   * with verified catalog recommendations.
   */
  async autoConfigure(options: AutoConfigureAuthOptions = {}): Promise<AutoConfigureAuthResult> {
    const env: Record<string, string | undefined> =
      options.env || (typeof process !== 'undefined' && (process as any)?.env ? (process as any).env : {});

    const configuredProviders: string[] = [];

    const providerKeyMap: Array<{ provider: string; envVar: string; fallbackEnv?: string }> = [
      { provider: 'openai', envVar: 'OPENAI_API_KEY' },
      { provider: 'anthropic', envVar: 'ANTHROPIC_API_KEY' },
      { provider: 'google', envVar: 'GEMINI_API_KEY', fallbackEnv: 'GOOGLE_API_KEY' },
      { provider: 'deepseek', envVar: 'DEEPSEEK_API_KEY' },
      { provider: 'groq', envVar: 'GROQ_API_KEY' },
      { provider: 'mistral', envVar: 'MISTRAL_API_KEY' },
    ];

    for (const mapping of providerKeyMap) {
      const key = env[mapping.envVar] || (mapping.fallbackEnv ? env[mapping.fallbackEnv] : undefined);
      if (key && key.trim()) {
        try {
          await this.credentials.save({
            provider: mapping.provider,
            type: 'api',
            api_key: key.trim(),
            active: true,
          });
          configuredProviders.push(mapping.provider);
        } catch {
          // Continue if already registered or error
        }
      }
    }

    let source: 'env' | 'existing_credential' | 'none' = configuredProviders.length > 0 ? 'env' : 'none';
    let primaryProvider: string | undefined = configuredProviders[0];

    // Check if Codex is authenticated via OAuth
    const codexAuthed = await this.codex.isAuthenticated();
    if (codexAuthed) {
      primaryProvider = 'codex';
      if (source === 'none') source = 'existing_credential';
    } else if (!primaryProvider) {
      // Check if existing credentials exist in daemon
      try {
        const creds = await this.credentials.list();
        const activeCred = creds.records.find((r) => r.active);
        if (activeCred) {
          primaryProvider = activeCred.provider;
          source = 'existing_credential';
        }
      } catch {}
    }

    let fleetApplied = false;
    if (primaryProvider && options.applyFleetRecommendations !== false) {
      try {
        const settings = new SwarmSettingsNamespace(this.transport);
        await settings.applyProviderFleet(primaryProvider);
        fleetApplied = true;
      } catch {
        // Fallback or retry
      }
    }

    return {
      configuredProviders,
      primaryProvider,
      source,
      fleetApplied,
    };
  }
}

export interface AutoConfigureAuthOptions {
  /** If true (default), automatically applies verified catalog recommendations to the fleet */
  applyFleetRecommendations?: boolean;
  /** Custom env map (defaults to process.env) */
  env?: Record<string, string | undefined>;
}

export interface AutoConfigureAuthResult {
  configuredProviders: string[];
  primaryProvider?: string;
  source: 'env' | 'existing_credential' | 'none';
  fleetApplied: boolean;
}
