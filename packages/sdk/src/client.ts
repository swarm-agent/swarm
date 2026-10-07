import { SwarmAppsNamespace } from './apps.js';
import { SwarmChatNamespace } from './chat.js';
import { SwarmPermissionsNamespace } from './permissions.js';
import { SwarmSettingsNamespace } from './settings.js';
import { SwarmOnboardingNamespace } from './provider-auth.js';
import { SwarmRealtimeNamespace } from './realtime.js';
import { SwarmUsageNamespace } from './usage.js';
import { SwarmAuthNamespace } from './auth.js';
import { SwarmAutomationsNamespace } from './automations.js';
import { SwarmWorkersNamespace } from './workers.js';
import { SwarmDeliverablesNamespace } from './deliverables.js';
import { SwarmDeployNamespace } from './deploy/index.js';
import { SwarmNotificationsNamespace } from './notifications.js';
import { SwarmProjectsNamespace } from './projects.js';
import { SwarmRemoteNamespace } from './remote.js';
import { SwarmSessionsNamespace } from './sessions.js';
import { SwarmSystemNamespace } from './system.js';
import { SwarmStorageNamespace } from './storage/index.js';
import { SwarmTransport } from './transport.js';
import type { ResolvedSwarmClientConfig, SwarmClientConfig } from './types.js';
import { SwarmWorkspacesNamespace } from './workspaces.js';

declare const process: any;

export class SwarmClient {
  readonly config: ResolvedSwarmClientConfig;
  readonly transport: SwarmTransport;
  readonly auth: SwarmAuthNamespace;
  readonly settings: SwarmSettingsNamespace;
  readonly onboarding: SwarmOnboardingNamespace;
  readonly realtime: SwarmRealtimeNamespace;
  /**
   * Legacy automations namespace targeting /v3/automations/v2.
   * Note: This is legacy automation API, not the canonical durable worker identity authority.
   */
  readonly automations: SwarmAutomationsNamespace;
  /**
   * Canonical durable worker namespace managing account-owned workers,
   * revisions, attached automations, and portable import/export definitions over /v3/workers.
   */
  readonly workers: SwarmWorkersNamespace;
  readonly usage: SwarmUsageNamespace;
  readonly deliverables: SwarmDeliverablesNamespace;
  /** Convenient alias for deliverables namespace: mailbox */
  readonly mailbox: SwarmDeliverablesNamespace;
  readonly workspaces: SwarmWorkspacesNamespace;
  readonly projects: SwarmProjectsNamespace;
  readonly sessions: SwarmSessionsNamespace;
  /** High-level interactive chat, response streaming, live tool events, and permission handling */
  readonly chat: SwarmChatNamespace;
  /** Account and session permission policy, bypass/permissionless mode, and pending approvals */
  readonly permissions: SwarmPermissionsNamespace;
  readonly apps: SwarmAppsNamespace;
  readonly system: SwarmSystemNamespace;
  readonly deploy: SwarmDeployNamespace;
  readonly notifications: SwarmNotificationsNamespace;
  /** Convenient alias for notifications namespace: inbox */
  readonly inbox: SwarmNotificationsNamespace;
  readonly storage: SwarmStorageNamespace;
  /** Owner administration of the relay connection (private transport only) */
  readonly remote: SwarmRemoteNamespace;
  /** Convenient alias for cloud storage connections: cloud */
  readonly cloud: SwarmStorageNamespace;

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
    this.settings = new SwarmSettingsNamespace(this.transport);
    this.onboarding = new SwarmOnboardingNamespace(this.transport);
    this.realtime = new SwarmRealtimeNamespace(this.transport);
    this.automations = new SwarmAutomationsNamespace(this.transport);
    this.workers = new SwarmWorkersNamespace(this.transport);
    this.usage = new SwarmUsageNamespace(this.transport);
    this.deliverables = new SwarmDeliverablesNamespace(this.transport);
    this.mailbox = this.deliverables;
    this.workspaces = new SwarmWorkspacesNamespace(this.transport);
    this.projects = new SwarmProjectsNamespace(this.transport);
    this.sessions = new SwarmSessionsNamespace(this.transport);
    this.chat = new SwarmChatNamespace(this.transport);
    this.permissions = new SwarmPermissionsNamespace(this.transport);
    this.apps = new SwarmAppsNamespace(this.transport);
    this.system = new SwarmSystemNamespace(this.transport);
    this.deploy = new SwarmDeployNamespace(this.transport);
    this.notifications = new SwarmNotificationsNamespace(this.transport);
    this.inbox = this.notifications;
    this.storage = new SwarmStorageNamespace(this.transport);
    this.cloud = this.storage;
    this.remote = new SwarmRemoteNamespace(this.transport);
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
