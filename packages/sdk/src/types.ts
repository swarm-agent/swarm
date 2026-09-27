export type TokenScope =
  | 'automations:trigger'
  | 'automations:read'
  | 'automations:write'
  | 'sessions:read'
  | 'sessions:write'
  | 'projects:read'
  | 'projects:write'
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

export type NotificationKind = 'system' | 'ai_request' | 'ai_deliverable' | string;
export type NotificationSeverity = 'info' | 'warning' | 'error' | string;
export type NotificationStatus = 'active' | 'resolved' | string;

export interface NotificationAction {
  id: string;
  label: string;
  action_type?: string;
  endpoint?: string;
  variant?: 'primary' | 'secondary' | 'danger' | string;
}

export interface NotificationRecord {
  id: string;
  swarm_id: string;
  origin_swarm_id?: string;
  session_id?: string;
  run_id?: string;
  category: string;
  kind?: NotificationKind;
  severity: NotificationSeverity;
  title: string;
  body: string;
  status: NotificationStatus;
  source_event_type?: string;
  permission_id?: string;
  tool_name?: string;
  requirement?: string;
  session_title?: string;
  session_label?: string;
  workspace_path?: string;
  workspace_name?: string;
  origin_label?: string;
  worker_id?: string;
  verified?: boolean;
  action_url?: string;
  payload?: Record<string, unknown>;
  actions?: NotificationAction[];
  read_at?: number;
  acked_at?: number;
  muted_at?: number;
  created_at: number;
  updated_at: number;
}

export interface NotificationSummary {
  swarm_id: string;
  total_count: number;
  unread_count: number;
  active_count: number;
  updated_at: number;
}

export interface SubmitNotificationParams {
  id?: string;
  swarm_id?: string;
  origin_swarm_id?: string;
  session_id?: string;
  run_id?: string;
  category?: string;
  kind?: NotificationKind;
  severity?: NotificationSeverity;
  title: string;
  body?: string;
  status?: NotificationStatus;
  action_url?: string;
  worker_id?: string;
  origin_label?: string;
  payload?: Record<string, unknown>;
  actions?: NotificationAction[];
}

export interface NotificationListFilter {
  limit?: number;
  swarm_id?: string;
}

export interface UpdateNotificationParams {
  read?: boolean;
  acked?: boolean;
  muted?: boolean;
  status?: string;
  swarm_id?: string;
}

// ============================================================================
// Projects, Tasks, and Orchestrate Primitives
// ============================================================================

export interface ProjectWorkspaceRef {
  workspace_id?: string;
  path: string;
  role?: 'primary_code' | 'auxiliary' | 'docs' | string;
  label?: string;
}

export interface ProjectTaskMediaRef {
  id: string;
  title?: string;
  url?: string;
  media_type?: string;
  kind?: 'image' | 'video' | 'audio' | 'doc' | string;
  filename?: string;
  data?: string;
  size_bytes?: number;
  created_at?: number;
}

export interface ProjectRecord {
  id: string;
  account_id?: string;
  name: string;
  description?: string;
  workspaces?: ProjectWorkspaceRef[];
  project_context?: string;
  active_task_ids?: string[];
  automation_ids?: string[];
  primary_session_id?: string;
  uploaded_media?: ProjectTaskMediaRef[];
  created_at: number;
  updated_at: number;
}

export interface CreateProjectParams {
  id?: string;
  name: string;
  description?: string;
  workspaces?: ProjectWorkspaceRef[];
  project_context?: string;
  active_task_ids?: string[];
  automation_ids?: string[];
  primary_session_id?: string;
  uploaded_media?: ProjectTaskMediaRef[];
}

export interface UpdateProjectParams {
  name?: string;
  description?: string;
  project_context?: string;
  primary_session_id?: string;
  workspaces?: ProjectWorkspaceRef[];
  active_task_ids?: string[];
  automation_ids?: string[];
}

export interface ListProjectsParams {
  limit?: number;
}

export interface SynthesizeProjectContextParams {
  name: string;
  workspaces: string[];
}

export interface DeleteProjectResult {
  status: string;
  id: string;
}

export interface ClearProjectContextResult {
  status: string;
  [key: string]: unknown;
}

export interface ProjectTaskDeliverable {
  id: string;
  title: string;
  kind: 'video' | 'code_diff' | 'artifact' | 'report' | string;
  status: 'ready' | 'accepted' | 'in_progress' | string;
  duration?: string;
  thumbnail?: string;
  description?: string;
  artifact_ref?: string;
  code_diff?: string;
  media_url?: string;
  parent_deliverable_id?: string;
  source_media_ref?: string;
}

export interface ProjectTaskScene {
  scene_number: number;
  title: string;
  duration_sec: number;
  prompt: string;
  visual_notes?: string;
}

export interface ProjectTaskPlanBinding {
  plan_id?: string;
  definition_revision?: number;
  session_id?: string;
  receipt?: string;
}

export interface ProjectTaskRecord {
  id: string;
  project_id: string;
  account_id?: string;
  title: string;
  description?: string;
  status:
    | 'queued'
    | 'in_progress'
    | 'needs_review'
    | 'completed'
    | 'failed'
    | 'pending_approval'
    | 'planning'
    | 'rejected'
    | string;
  session_id?: string;
  agent?: string;
  worker_name?: string;
  outcome_type?: string;
  workspace_path?: string;
  worktree_branch?: string;
  worktree_name?: string;
  base_branch?: string;
  base_commit?: string;
  git_status?: 'clean' | 'dirty' | 'diverged' | 'unknown' | 'stale' | string;
  unintegrated_commits?: number;
  behind_commits?: number;
  is_integrated?: boolean;
  diff_summary?: string;
  is_dirty?: boolean;
  dirty_count?: number;
  sync_warning?: string;
  action_needed?: string;
  what_did_do?: string[];
  what_not_done?: string[];
  pipeline_stages?: string[];
  current_stage_index?: number;
  deliverables?: ProjectTaskDeliverable[];
  workspaces_involved?: string[];
  context_pool_summary?: string;
  plan_summary?: string;
  full_plan_markdown?: string;
  tier?: string;
  feature_size?: 'small' | 'big' | string;
  revision?: number;
  last_error?: string;
  feedback_history?: string[];
  aspect_ratio?: string;
  resolution?: string;
  variant_count?: number;
  duration_seconds?: number;
  model?: string;
  provider?: string;
  thinking?: string;
  service_tier?: string;
  context_mode?: string;
  scenes?: ProjectTaskScene[];
  soundtrack?: string;
  auto_approve?: boolean;
  router_alert?: string;
  attached_media?: ProjectTaskMediaRef[];
  plan_binding?: ProjectTaskPlanBinding;
  plan_document?: any;
  task_program?: any;
  task_program_id?: string;
  task_program_status?: any;
  created_at: number;
  updated_at: number;
}

export interface CreateProjectTaskParams {
  id?: string;
  title: string;
  description?: string;
  prompt?: string;
  agent?: string;
  worker_name?: string;
  feature_size?: 'small' | 'big' | string;
  workspace_path?: string;
  worktree_branch?: string;
  outcome_type?: string;
  tier?: string;
  aspect_ratio?: string;
  resolution?: string;
  variant_count?: number;
  deliverable_count?: number;
  duration_seconds?: number;
  model?: string;
  provider?: string;
  thinking?: string;
  service_tier?: string;
  context_mode?: string;
  soundtrack?: string;
  auto_approve?: boolean;
  pipeline_stages?: string[];
  deliverables?: ProjectTaskDeliverable[];
  what_did_do?: string[];
  what_not_done?: string[];
  attached_media?: ProjectTaskMediaRef[];
  document?: any;
  plan_document?: any;
  task_program?: any;
  task_program_id?: string;
  plan_summary?: string;
  full_plan_markdown?: string;
  diff_summary?: string;
  intent?: string;
  video_type?: string;
  enhance_prompt?: boolean;
  scenes_count?: number;
}

export interface UpdateProjectTaskParams {
  title?: string;
  description?: string;
  agent?: string;
  worker_name?: string;
  outcome_type?: string;
  workspace_path?: string;
  worktree_branch?: string;
  worktree_name?: string;
  base_branch?: string;
  behind_commits?: number;
  is_integrated?: boolean;
  dirty_count?: number;
  sync_warning?: string;
  unintegrated_commits?: number;
  diff_summary?: string;
  is_dirty?: boolean;
  action_needed?: string;
  what_did_do?: string[];
  what_not_done?: string[];
  pipeline_stages?: string[];
  current_stage_index?: number;
  deliverables?: ProjectTaskDeliverable[];
  workspaces_involved?: string[];
  plan_summary?: string;
  full_plan_markdown?: string;
  tier?: string;
  feature_size?: string;
  last_error?: string;
  revision?: number;
  task_program?: any;
  task_program_id?: string;
  model?: string;
  provider?: string;
  thinking?: string;
  service_tier?: string;
  context_mode?: string;
}

export interface CreateProjectTaskResult {
  task: ProjectTaskRecord;
  model_preview?: any;
}

export interface GetProjectTaskResult {
  task: ProjectTaskRecord;
  model_preview?: any;
}

export interface DeleteProjectTaskResult {
  status: string;
  task_id: string;
}

export interface PreviewProjectTaskParams {
  prompt?: string;
  title?: string;
  intent?: string;
  feature_size?: string;
  agent?: string;
  outcome_type?: string;
  tier?: string;
  model?: string;
  provider?: string;
  thinking?: string;
  service_tier?: string;
  context_mode?: string;
  workspace_path?: string;
}

export interface ProjectTaskPreviewResult {
  task_plan: any;
  model_preview?: any;
}

export interface ProjectTaskModelPreviewResult {
  task: ProjectTaskRecord;
  model_preview: any;
}

export interface ProjectTaskApprovalGuards {
  session_id?: string;
  plan_id?: string;
  definition_revision?: number;
}

export interface ApproveProjectTaskResult {
  status: 'approved' | 'already_approved' | string;
  task: ProjectTaskRecord;
}

export interface RejectProjectTaskResult {
  status: 'rejected' | string;
  task: ProjectTaskRecord;
}

export interface ReopenProjectTaskResult {
  status: 'reopened' | string;
  task: ProjectTaskRecord;
}

export interface CompleteProjectTaskResult {
  status: 'completed' | string;
  task: ProjectTaskRecord;
}

export interface RefineProjectTaskParams extends ProjectTaskApprovalGuards {
  feedback?: string;
  error_summary?: string;
  agent?: string;
  feature_size?: string;
  outcome_type?: string;
  tier?: string;
  model?: string;
}

export interface RefineProjectTaskResult {
  status: 'refined' | string;
  task: ProjectTaskRecord;
}

export interface IntegrateProjectTaskResult {
  status: 'integrated' | 'already_integrated' | string;
  message?: string;
  task?: ProjectTaskRecord;
  integration_plan?: any;
  resulting_target_head?: string;
  [key: string]: unknown;
}

export interface DeployProjectTaskProgramResult {
  status: 'deployed' | string;
  task: ProjectTaskRecord;
}

export interface RedeployProjectTaskJobResult {
  status: 'redeploying' | string;
  task: ProjectTaskRecord;
}

export interface GetProjectTaskProgramResult {
  program: any;
}

export interface DeleteProjectMediaResult {
  removed: boolean;
  media_id: string;
  uploaded_media: ProjectTaskMediaRef[];
}

export interface SyncStreamParams {
  endpoint_cursor: string;
  session_ids?: string[];
  surface?: string;
  selector_kind?: string;
  global?: boolean;
  limit?: number;
  include_active?: boolean;
  resources?: {
    notifications?: boolean;
    notification_summary?: boolean;
    tasks?: boolean;
    auth?: boolean;
  };
}

export interface SyncStreamResult<TEvent = any> {
  ok: boolean;
  endpoint_cursor: string;
  events: TEvent[];
  has_more: boolean;
  selector?: any;
  replay_instructions?: Record<string, unknown>;
}

