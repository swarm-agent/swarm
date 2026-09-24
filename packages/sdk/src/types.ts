export type TokenScope =
  | 'automations:trigger'
  | 'automations:read'
  | 'automations:write'
  | 'sessions:read'
  | 'sessions:write'
  | 'admin'
  | string;

export interface SwarmClientConfig {
  /** Base URL for the Swarm API/Desktop daemon (e.g. http://127.0.0.1:18080 or http://127.0.0.1:7781) */
  baseUrl?: string;
  /** Bearer authentication token (attach token or scoped token starting with 'swk_') */
  token?: string;
  /** Unix domain socket path for zero-conf local IPC (e.g. /run/swarmd/swarmd.sock) */
  socketPath?: string;
  /** Custom default HTTP headers */
  defaultHeaders?: Record<string, string>;
  /** Request timeout in milliseconds (default: 30,000ms) */
  timeoutMs?: number;
}

export interface ResolvedSwarmClientConfig {
  baseUrl: string;
  token?: string;
  socketPath?: string;
  defaultHeaders: Record<string, string>;
  timeoutMs: number;
}

export interface RequestOptions {
  method?: string;
  headers?: Record<string, string>;
  body?: unknown;
  signal?: AbortSignal;
  timeoutMs?: number;
}

export interface ScopedTokenRecord {
  id: string;
  name: string;
  token_hint: string;
  scopes: TokenScope[];
  worker_id?: string;
  worker_name?: string;
  expires_at?: number;
  last_used_at?: number;
  created_at: number;
  revoked?: boolean;
  revoked_at?: number;
}

export interface CreateScopedTokenParams {
  name: string;
  scopes: TokenScope[];
  worker_id?: string;
  worker_name?: string;
  expires_in_seconds?: number;
}

export interface CreateScopedTokenResult {
  ok: boolean;
  token: string;
  record: ScopedTokenRecord;
}

export interface DesktopSessionBootstrap {
  ok?: boolean;
  token: string;
  user_id: string;
  account_scope_id: string;
  mode?: string;
}

export interface AutomationV2TriggerParams {
  workspace_id?: string;
  worker_id?: string;
  session_id?: string;
  automation_id?: string;
  context?: Record<string, unknown>;
}

export interface AutomationV2TriggerResult {
  ok: boolean;
  occurrence: AutomationV2Occurrence;
  message?: string;
}

export interface AutomationV2Settings {
  schema_version: 2;
  schedule: {
    kind: 'interval' | 'cron' | 'trigger';
    interval_seconds?: number;
    cron?: string;
    timezone?: string;
  };
  expiration?: {
    kind: 'indefinite' | 'at';
    expires_at?: number;
  };
  missed?: 'skip' | 'coalesce';
  overlap?: 'serialize' | 'independent';
  daily_run_cap?: number;
  webhooks?: Array<{
    id?: string;
    url: string;
    secret?: string;
    format?: 'generic' | 'slack' | 'discord' | 'telegram';
    events?: string[];
    enabled?: boolean;
  }>;
}

export interface AutomationV2Record {
  automation_id: string;
  account_id: string;
  workspace_id: string;
  session_id: string;
  generation: number;
  enabled: boolean;
  cancelled: boolean;
  archived?: boolean;
  archived_at?: number;
  accepted_at?: number;
  next_due_at?: number;
  document?: {
    title: string;
    info: { goal: string; [key: string]: unknown };
    checkpoints: Array<{
      id: string;
      title: string;
      tasks?: string[];
      acceptance_criteria?: string[];
      [key: string]: unknown;
    }>;
    automation_v2?: AutomationV2Settings;
    worker_v2?: AutomationV2Settings;
    [key: string]: unknown;
  };
}

export interface AutomationV2Occurrence {
  id: string;
  state: string;
  due_at: number;
  session_id: string;
  accepted?: AutomationV2Record;
  run_id?: string;
  admitted_at?: number;
  observed_at?: number;
  closing_state?: string;
  summary?: string;
  result?: string;
  report?: string;
  trigger_context?: Record<string, unknown>;
  deliverables?: Array<{
    label?: string;
    path?: string;
    media_type?: string;
    filename?: string;
    artifact_id?: string;
    revision_ref?: string;
    [key: string]: unknown;
  }>;
}

export interface AutomationV2Progress {
  record: AutomationV2Record;
  observed_at: number;
  timezone: string;
  forecast: number[];
  forecast_is_admission: boolean;
  no_next_reason?: string;
  complete: boolean;
  next_cursor?: string;
  occurrences: AutomationV2Occurrence[];
}

export interface AutomationV2ListParams {
  workspace_id?: string;
  archived_mode?: 'exclude' | 'include' | 'only';
  cursor?: string;
  limit?: number;
}

export interface DeliverableActionContract {
  action: string;
  target_url?: string;
  target_secret_ref?: string;
  parameters?: Record<string, unknown>;
}

export interface DeliverableRevisionFeedback {
  requested_at: number;
  requested_by?: string;
  notes: string;
  tags?: string[];
}

export interface DeliverableRecord {
  id: string;
  account_id: string;
  workspace_id?: string;
  workspace_path?: string;
  worker_id?: string;
  occurrence_id?: string;
  session_id?: string;
  title: string;
  kind: 'social_post' | 'alert' | 'report' | 'pr_patch' | 'media_bundle' | 'code_patch' | 'media' | 'custom' | string;
  status: 'pending_review' | 'approved' | 'rejected' | 'published' | 'dismissed' | 'needs_revision' | string;
  summary?: string;
  payload?: Record<string, unknown>;
  media_refs?: Array<{
    label?: string;
    path?: string;
    media_type?: string;
    filename?: string;
    artifact_id?: string;
    revision_ref?: string;
    [key: string]: unknown;
  }>;
  action_contract?: DeliverableActionContract;
  action_result?: Record<string, unknown>;
  revision_feedback?: DeliverableRevisionFeedback;
  revision_history?: DeliverableRevisionFeedback[];
  created_at: number;
  updated_at: number;
  reviewed_at?: number;
  reviewed_by?: string;
}

export interface CreateDeliverableParams {
  id?: string;
  workspace_id?: string;
  workspace_path?: string;
  worker_id?: string;
  occurrence_id?: string;
  session_id?: string;
  title: string;
  kind?: string;
  status?: string;
  summary?: string;
  payload?: Record<string, unknown>;
  media_refs?: DeliverableRecord['media_refs'];
  action_contract?: DeliverableActionContract;
}

export interface DeliverableFilter {
  status?: string;
  worker_id?: string;
  kind?: string;
  workspace_id?: string;
  limit?: number;
}

export interface AutomationV2WebhookRecord {
  id: string;
  account_id?: string;
  workspace_id?: string;
  worker_id?: string;
  url: string;
  secret?: string;
  format?: 'generic' | 'slack' | 'discord' | 'telegram';
  events?: string[];
  enabled: boolean;
  created_at?: number;
  updated_at?: number;
}

export interface CreateWebhookParams {
  id?: string;
  workspace_id?: string;
  worker_id?: string;
  url: string;
  secret?: string;
  format?: 'generic' | 'slack' | 'discord' | 'telegram';
  events?: string[];
  enabled?: boolean;
}

export interface WebhookTestParams {
  id?: string;
  url?: string;
  secret?: string;
  format?: 'generic' | 'slack' | 'discord' | 'telegram';
  events?: string[];
  worker_id?: string;
  worker_title?: string;
}

export interface WebhookTestResult {
  ok: boolean;
  error?: string;
  result?: {
    status_code: number;
    status: string;
    duration_ms: number;
    body?: string;
    signature?: string;
  };
}

export interface SessionRecord {
  id: string;
  title: string;
  workspace_id?: string;
  workspace_path?: string;
  user_id?: string;
  account_scope_id?: string;
  state?: string;
  mode?: 'auto' | 'plan';
  agent_name?: string;
  created_at?: number;
  updated_at?: number;
  archived?: boolean;
  archived_at?: number;
  last_run_id?: string;
}

export interface CreateSessionParams {
  title?: string;
  workspace_id?: string;
  workspace_path?: string;
  agent_name?: string;
  agent?: string; // alias for agent_name
  mode?: 'auto' | 'plan';
  client_request_id?: string;
  metadata?: Record<string, unknown>;
}

export interface ListSessionsParams {
  limit?: number;
  state?: string;
  workspace_id?: string;
  category?: 'video' | 'needs_review' | 'blocked' | 'in_progress' | 'active_chats' | 'archived';
  cursor?: string;
}

export interface SessionMessage {
  id?: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  created_at?: number;
}

export interface SessionDetail {
  id: string;
  title: string;
  workspace_id?: string;
  workspace_path?: string;
  state?: string;
  mode?: 'auto' | 'plan';
  agent_name?: string;
  created_at: number;
  updated_at: number;
  archived?: boolean;
  messages?: SessionMessage[];
  last_run_id?: string;
  active_plan?: unknown;
  raw?: Record<string, unknown>;
}

export interface WorkspaceRecord {
  workspace_id: string;
  workspace_name?: string;
  name?: string;
  path: string;
  state?: string;
  is_git_repo?: boolean;
  worktree_enabled?: boolean;
  directories?: string[];
  created_at?: number;
  updated_at?: number;
  is_default?: boolean;
  active?: boolean;
  git?: {
    branch?: string;
    clean?: boolean;
    dirty_files?: number;
  };
}

export interface SystemHealth {
  ok: boolean;
  status?: string;
  version?: string;
  uptime_seconds?: number;
}
