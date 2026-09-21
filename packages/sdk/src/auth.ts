import type { SwarmTransport } from './transport.js';
import type {
  CreateScopedTokenParams,
  CreateScopedTokenResult,
  DesktopSessionBootstrap,
  ScopedTokenRecord,
} from './types.js';

export class SwarmAuthNamespace {
  private transport: SwarmTransport;
  private onTokenUpdate?: (token: string) => void;

  constructor(transport: SwarmTransport, onTokenUpdate?: (token: string) => void) {
    this.transport = transport;
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
        expires_in_seconds: params.expires_in_seconds,
      },
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
}
