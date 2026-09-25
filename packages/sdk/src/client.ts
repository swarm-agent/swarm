import { SwarmAuthNamespace } from './auth.js';
import { SwarmAutomationsNamespace } from './automations.js';
import { SwarmDeliverablesNamespace } from './deliverables.js';
import { SwarmDeployNamespace } from './deploy/index.js';
import { SwarmNotificationsNamespace } from './notifications.js';
import { SwarmSessionsNamespace } from './sessions.js';
import { SwarmSystemNamespace } from './system.js';
import { SwarmTransport } from './transport.js';
import type { ResolvedSwarmClientConfig, SwarmClientConfig } from './types.js';
import { SwarmWorkspacesNamespace } from './workspaces.js';

declare const process: any;

export class SwarmClient {
  readonly config: ResolvedSwarmClientConfig;
  readonly transport: SwarmTransport;
  readonly auth: SwarmAuthNamespace;
  readonly automations: SwarmAutomationsNamespace;
  /** Convenient alias for automations namespace */
  readonly workers: SwarmAutomationsNamespace;
  readonly deliverables: SwarmDeliverablesNamespace;
  /** Convenient alias for deliverables namespace: mailbox */
  readonly mailbox: SwarmDeliverablesNamespace;
  readonly workspaces: SwarmWorkspacesNamespace;
  readonly sessions: SwarmSessionsNamespace;
  readonly system: SwarmSystemNamespace;
  readonly deploy: SwarmDeployNamespace;
  readonly notifications: SwarmNotificationsNamespace;
  /** Convenient alias for notifications namespace: inbox */
  readonly inbox: SwarmNotificationsNamespace;

  constructor(config: SwarmClientConfig = {}) {
    const env: Record<string, string | undefined> =
      typeof process !== 'undefined' && process?.env ? process.env : {};

    const baseUrl =
      config.baseUrl ||
      env.SWARM_API_URL ||
      env.SWARM_DESKTOP_URL ||
      'http://127.0.0.1:18080';

    const socketPath = config.socketPath || env.SWARM_SOCKET_PATH;
    const token = config.token || env.SWARM_AUTH_TOKEN || env.SWARM_DEPLOY_TOKEN;

    const resolved: ResolvedSwarmClientConfig = {
      baseUrl: baseUrl.replace(/\/+$/, ''),
      token,
      socketPath,
      defaultHeaders: config.defaultHeaders ?? {},
      timeoutMs: config.timeoutMs ?? 30_000,
    };

    this.transport = new SwarmTransport(resolved);
    this.config = this.transport.getConfig();

    this.auth = new SwarmAuthNamespace(this.transport, (token: string) => {
      this.config.token = token;
    });
    this.automations = new SwarmAutomationsNamespace(this.transport);
    this.workers = this.automations;
    this.deliverables = new SwarmDeliverablesNamespace(this.transport);
    this.mailbox = this.deliverables;
    this.workspaces = new SwarmWorkspacesNamespace(this.transport);
    this.sessions = new SwarmSessionsNamespace(this.transport);
    this.system = new SwarmSystemNamespace(this.transport);
    this.deploy = new SwarmDeployNamespace(this.transport);
    this.notifications = new SwarmNotificationsNamespace(this.transport);
    this.inbox = this.notifications;
  }

  /**
   * Updates the bearer authentication token for all subsequent client requests.
   */
  setToken(token: string): void {
    this.transport.setConfig({ token });
    this.config.token = token;
  }
}

/**
 * Factory function to create a new SwarmClient instance.
 */
export function createSwarmClient(config?: SwarmClientConfig): SwarmClient {
  return new SwarmClient(config);
}
