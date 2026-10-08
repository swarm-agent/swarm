import type { SwarmTransport } from './transport.js';

/** An AI client's authorization request waiting on this machine's owner. */
export interface RemoteConsent {
  code: string;
  client_id: string;
  client_name: string;
  client_domain?: string;
  redirect_host: string;
  scopes: string[];
  expires_at: number;
}

/** Relay connection state. Never contains the device key or token. */
export interface RemoteStatus {
  configured: boolean;
  enabled: boolean;
  connected: boolean;
  relay_url?: string;
  device_id?: string;
  device_name?: string;
  public_key?: string;
  allow_write: boolean;
  allow_approve: boolean;
  allow_manage: boolean;
  last_error?: string;
  pending_consents: RemoteConsent[];
  /** Set while the relay waits for an authorized client to pair this machine. */
  pairing_code?: string;
  pairing_expires_at?: number;
}

export interface RemoteInitInput {
  relay_url: string;
  device_name: string;
  allow_write?: boolean;
  allow_approve?: boolean;
  allow_manage?: boolean;
}

/**
 * Owner administration of the machine's relay connection (`/v1/remote*`).
 * The daemon refuses scoped tokens here: call it only from a trusted backend
 * on the private local transport, never from a browser.
 */
export class SwarmRemoteNamespace {
  constructor(private readonly transport: SwarmTransport) {}

  private async call(path: string, method: 'GET' | 'POST', body?: unknown): Promise<RemoteStatus> {
    const res = await this.transport.request<{ remote: RemoteStatus }>(`/v1/remote${path}`, { method, ...(method === 'POST' ? { body: body ?? {} } : {}) });
    return res.data.remote;
  }

  status(): Promise<RemoteStatus> {
    return this.call('', 'GET');
  }

  /** Creates this machine's device key for one relay. Does not connect. */
  init(input: RemoteInitInput): Promise<RemoteStatus> {
    return this.call('/init', 'POST', {
      relay_url: input.relay_url, device_name: input.device_name,
      allow_write: input.allow_write === true, allow_approve: input.allow_approve === true, allow_manage: input.allow_manage === true,
    });
  }

  enable(): Promise<RemoteStatus> {
    return this.call('/enable', 'POST');
  }

  disable(): Promise<RemoteStatus> {
    return this.call('/disable', 'POST');
  }

  /**
   * Approves or denies an AI client's authorization request shown on the
   * relay's consent page. Approved scopes are clamped to the request and to
   * this machine's ceiling by the daemon.
   */
  async decideConsent(code: string, approve: boolean, scopes?: string[]): Promise<RemoteConsent> {
    const res = await this.transport.request<{ consent: RemoteConsent }>(`/v1/remote/consents/${approve ? 'approve' : 'deny'}`, {
      method: 'POST', body: approve && scopes ? { code, scopes } : { code },
    });
    return res.data.consent;
  }

  /** Disconnects, revokes the device token and deletes the device key. */
  reset(): Promise<RemoteStatus> {
    return this.call('/reset', 'POST');
  }
}
