import {
  requestJson,
  apiFetch,
  readErrorMessage,
  ensureDesktopSession,
} from "../../../../app/api";
import type {
  DesktopPermissionRecord,
  DesktopSessionRecord,
  DesktopSessionUsageRecord,
} from "../../types/realtime";
import type {
  AgentStateRecord,
  AgentToolInventoryRecord,
  AgentToolContractRecord,
  AgentToolContractRuntimeRecord,
  ResolvedAgentToolContractRecord,
  ChatMessageRecord,
  ModelOptionRecord,
  ProviderDefaultsPreviewRecord,
  ResolvedSessionPreference,
  DesktopSessionPlanDocument,
  DesktopSessionPlanRecord,
  DesktopSessionPlanRevisionRecord,
} from "../types/chat";
import {
  applyDesktopChatRouteToSession,
  desktopChatRouteFromSessionMetadata,
  getDesktopSessionCreateV2Target,
  isLocalContainerDesktopChatRoute,
  isManagedHostDesktopChatRoute,
  isPrimaryDesktopChatRoute,
  type DesktopChatRoute,
} from "../services/chat-routing";
import {
  canonicalSessionWorkspaceName,
  canonicalSessionWorkspacePath,
  sessionWorkspaceFactsFromMetadata,
} from "../../services/session-workspace";
import {
  modelAllowedByProviderPreset,
  sortModelOptions,
  supportsCodex1MMode,
} from "../services/model-options";
import { parseStructuredToolMessage } from "../services/tool-message";

interface SessionWire {
  id?: string;
  title?: string;
  workspace_path?: string;
  workspace_name?: string;
  mode?: string;
  metadata?: Record<string, unknown>;
  session_api?: string;
  last_event_seq?: number;
  projection_high_watermark_seq?: number;
  message_count?: number;
  updated_at?: number;
  created_at?: number;
  worktree_enabled?: boolean;
  worktree_root_path?: string;
  worktree_base_branch?: string;
  worktree_branch?: string;
  git_branch?: string;
  git_has_git?: boolean;
  git_clean?: boolean;
  git_dirty_count?: number;
  git_staged_count?: number;
  git_modified_count?: number;
  git_untracked_count?: number;
  git_conflict_count?: number;
  git_ahead_count?: number;
  git_behind_count?: number;
  git_commit_detected?: boolean;
  git_commit_count?: number;
  git_committed_file_count?: number;
  git_committed_additions?: number;
  git_committed_deletions?: number;
  lifecycle?: {
    session_id?: string;
    run_id?: string;
    active?: boolean;
    phase?: string;
    started_at?: number;
    ended_at?: number;
    updated_at?: number;
    generation?: number;
    stop_reason?: string;
    error?: string;
    owner_transport?: string;
  } | null;
}

interface ResolvePermissionResponseWire {
  permission?: {
    id?: string;
    session_id?: string;
    run_id?: string;
    call_id?: string;
    tool_name?: string;
    tool_arguments?: string;
    approved_arguments?: string;
    status?: string;
    decision?: string;
    reason?: string;
    requirement?: string;
    mode?: string;
    created_at?: number;
    updated_at?: number;
    resolved_at?: number;
    permission_requested_at?: number;
  };
  saved_rule?: {
    id?: string;
    kind?: string;
    decision?: string;
    tool?: string;
    pattern?: string;
    created_at?: number;
    updated_at?: number;
  };
}

interface PendingPermissionsResponseWire {
  permissions?: ResolvePermissionResponseWire["permission"][];
}

interface MessageWire {
  id?: string;
  session_id?: string;
  global_seq?: number;
  role?: string;
  content?: string;
  created_at?: number;
  metadata?: Record<string, unknown>;
}

interface MessagesResponseWire {
  messages?: MessageWire[];
}

interface SessionProjectionWire {
  session_id?: string;
  last_event_seq?: number;
  projection_high_watermark_seq?: number;
  updated_at?: number;
}

interface V3HydratedSessionResponseWire {
  session?: SessionWire;
  projection?: SessionProjectionWire;
  messages?: MessagesResponseWire["messages"];
  events?: unknown[];
}

interface V3RunIntentWire {
  session_id?: string;
  run_id?: string;
  status?: string;
  blocked_reason?: string;
  created_at?: number;
  updated_at?: number;
  event_seq?: number;
}

interface V3MessageCommitResponseWire extends V3HydratedSessionResponseWire {
  ok?: boolean;
  message?: MessageWire;
  run_intent?: V3RunIntentWire | null;
}

export interface V3RunIntentRecord {
  sessionId: string;
  runId: string;
  status: string;
  blockedReason: string;
  createdAt: number;
  updatedAt: number;
  eventSeq: number;
}

export interface SendSessionMessageResult {
  ok?: boolean;
  session?: DesktopSessionRecord;
  message?: ChatMessageRecord | null;
  messages?: ChatMessageRecord[];
  runIntent?: V3RunIntentRecord | null;
  events?: unknown[];
}

interface SessionDataRequestOptions {
  sessionApi?: string | null;
}

const V3_SESSION_ID_PREFIX = "v3session_";

export function isV3SessionId(sessionId: string | null | undefined): boolean {
  return (sessionId ?? "").trim().startsWith(V3_SESSION_ID_PREFIX);
}

function resolveSessionApiForSession(
  sessionId: string,
  options: SessionDataRequestOptions = {},
): string {
  const sessionApi = options.sessionApi?.trim().toLowerCase() ?? "";
  if (sessionApi === "v3" || isV3SessionId(sessionId)) {
    return "v3";
  }
  return sessionApi;
}

function rejectV3SessionV2Subresource(sessionId: string, subresource: string): void {
  const normalizedSessionId = sessionId.trim();
  if (!isV3SessionId(normalizedSessionId)) {
    return;
  }
  throw new Error(
    `Sessions API v3 does not support ${subresource}; refusing to call legacy Sessions API v2 for ${normalizedSessionId}.`,
  );
}

interface SendSessionMessageOptions extends SessionDataRequestOptions {
  clientRequestId?: string | null;
}

interface SessionPreferenceWire {
  preference?: {
    provider?: string;
    model?: string;
    thinking?: string;
    service_tier?: string;
    context_mode?: string;
    updated_at?: number;
  };
  context_window?: number;
  max_output_tokens?: number;
}

interface SessionMetadataWire {
  metadata?: Record<string, unknown>;
}

interface SessionCodexConfigWire {
  session_id?: string;
  provider?: string;
  model?: string;
  thinking?: string;
  service_tier?: string;
  context_mode?: string;
  effective_context_window?: number;
  updated_at?: number;
}

export interface DesktopSessionCodexConfig {
  sessionId: string;
  provider: string;
  model: string;
  thinking: string;
  serviceTier: string;
  contextMode: string;
  effectiveContextWindow: number;
  updatedAt: number;
}

interface DraftModelWire {
  preference?: {
    provider?: string;
    model?: string;
    thinking?: string;
    service_tier?: string;
    context_mode?: string;
    updated_at?: number;
  };
  context_window?: number;
  max_output_tokens?: number;
}

interface SessionUsageSummaryWire {
  session_id?: string;
  provider?: string;
  model?: string;
  source?: string;
  context_window?: number;
  total_tokens?: number;
  remaining_tokens?: number;
  updated_at?: number;
}

interface SessionUsageResponseWire {
  usage_summary?: SessionUsageSummaryWire | null;
}

interface SessionPlanInfoWire {
  goal?: string;
  scope?: string;
  context?: string;
  decisions?: string[];
  constraints?: string[];
  assumptions?: string[];
  open_questions?: string[];
  relevant_files?: string[];
  files?: string[];
  success_criteria?: string[];
  validation_strategy?: string;
  validation?: string | string[];
}

interface SessionPlanCheckpointWire {
  id?: string;
  title?: string;
  status?: string;
  objective?: string;
  tasks?: string[];
  acceptance_criteria?: string[];
  notes?: string;
  report?: string;
  result?: string;
  changed_files?: string[];
  validation?: string[];
  order?: number;
}

interface SessionPlanDocumentWire {
  id?: string;
  title?: string;
  status?: string;
  schema_version?: string;
  revision_id?: string;
  info?: SessionPlanInfoWire | null;
  checkpoints?: SessionPlanCheckpointWire[] | null;
  active_checkpoint_id?: string;
  rendered_text?: string;
  display_text?: string;
}

interface SessionPlanWire {
  id?: string;
  title?: string;
  plan?: string;
  document?: SessionPlanDocumentWire | null;
  status?: string;
  approval_state?: string;
  created_at?: number;
  updated_at?: number;
  prior_title?: string;
  prior_plan?: string;
  diff_lines?: string[];
  update_summary?: string;
  update_scope?: string;
  update_kind?: string;
  version?: number;
  parent_revision?: number;
  checkpoint?: boolean;
}

interface ActiveSessionPlanResponseWire {
  has_active?: boolean;
  active_plan?: SessionPlanWire | null;
}

interface SaveSessionPlanResponseWire {
  plan?: SessionPlanWire | null;
}

interface SessionPlansResponseWire {
  active_plan_id?: string;
  plans?: SessionPlanWire[] | null;
}

interface SessionPlanHistoryResponseWire {
  revisions?: SessionPlanWire[] | null;
}

interface ResolveAllPermissionsResponseWire {
  resolved?: ResolvePermissionResponseWire["permission"][];
}

type ProviderDefaultsPreviewWire = {
  provider?: string;
  primary_agent?: string;
  primary_model?: string;
  primary_thinking?: string;
  utility_provider?: string;
  utility_model?: string;
  utility_thinking?: string;
  utility_agents?: string[];
  affected_agents?: string[];
  out_of_sync_agents?: string[];
  inheriting_agents?: string[];
  stale_inherited_agents?: string[];
  custom_utility_agents?: string[];
  utility_baseline_agents?: string[];
  overwrite_explicit?: boolean;
};

type AgentToolContractWire = {
  preset?: string;
  inherit_policy?: boolean;
  tools?: Record<
    string,
    {
      enabled?: boolean;
      bash_prefixes?: string[];
    }
  >;
};

type ResolvedAgentToolContractWire = {
  runtime_mode?: string;
  raw_preset?: string;
  inherit_policy?: boolean;
  available_tools?: string[];
  unavailable_tools?: string[];
  tools?: Record<
    string,
    {
      enabled?: boolean;
      bash_prefixes?: string[];
      source?: string;
    }
  >;
};

type AgentToolContractResponseWire = {
  agent?: string;
  raw_tool_contract?: AgentToolContractWire | null;
  resolved?: ResolvedAgentToolContractWire | null;
  compiled_policy?: unknown;
  tool_inventory?: AgentToolInventoryWire | null;
};

type AgentToolInventoryWire = {
  tools?: Array<{
    name?: string;
    contract_name?: string;
    description?: string;
    group?: string;
    kind?: string;
  }>;
  presets?: Array<{
    id?: string;
    label?: string;
    description?: string;
    enabled_tools?: string[];
    disabled_by_default?: string[];
    bash_prefixes?: string[];
  }>;
};

type AgentStateWire = {
  state?: {
    profiles?: Array<{
      name?: string;
      mode?: string;
      description?: string;
      provider?: string;
      model?: string;
      thinking?: string;
      prompt?: string;
      runtime_mode?: string;
      execution_setting?: string;
      exit_plan_mode_enabled?: boolean;
      tool_scope?: {
        preset?: string;
        allow_tools?: string[];
        deny_tools?: string[];
        bash_prefixes?: string[];
        inherit_policy?: boolean;
      } | null;
      tool_contract?: AgentToolContractWire | null;
      enabled?: boolean;
      protected?: boolean;
      updated_at?: number;
    }>;
    active_primary?: string;
    active_subagent?: Record<string, string>;
    version?: number;
  };
  provider_defaults_preview?: ProviderDefaultsPreviewWire | null;
  tool_inventory?: AgentToolInventoryWire | null;
};

type RestoreAgentDefaultsWire = {
  ok?: boolean;
  provider_defaults_preview?: ProviderDefaultsPreviewWire | null;
  profiles?: Array<{
    name?: string;
    mode?: string;
    description?: string;
    provider?: string;
    model?: string;
    thinking?: string;
    prompt?: string;
    runtime_mode?: string;
    execution_setting?: string;
    exit_plan_mode_enabled?: boolean;
    enabled?: boolean;
    protected?: boolean;
    updated_at?: number;
  }>;
  active_primary?: string;
  active_subagent?: Record<string, string>;
  version?: number;
};

interface ProviderStatusWire {
  id?: string;
  ready?: boolean;
  runnable?: boolean;
}

interface ProvidersResponseWire {
  providers?: ProviderStatusWire[];
}

interface FavoriteRecordWire {
  provider?: string;
  model?: string;
  label?: string;
  thinking?: string;
}

interface FavoritesResponseWire {
  records?: FavoriteRecordWire[];
}

interface ModelCatalogRecordWire {
  provider?: string;
  model?: string;
  context_window?: number;
}

interface CatalogResponseWire {
  records?: ModelCatalogRecordWire[];
}

function emptyLiveState(): DesktopSessionRecord["live"] {
  return {
    runId: null,
    agentName: null,
    startedAt: null,
    status: "idle",
    step: 0,
    toolName: null,
    toolCallId: null,
    toolArguments: null,
    toolOutput: "",
    retainedToolName: null,
    retainedToolCallId: null,
    retainedToolArguments: null,
    retainedToolOutput: "",
    retainedToolState: null,
    summary: null,
    lastEventType: null,
    lastEventAt: null,
    error: null,
    seq: 0,
    assistantDraft: "",
    retainedAssistantSegments: [],
    reasoningSummary: "",
    reasoningText: "",
    reasoningState: "idle",
    reasoningSegment: 0,
    reasoningStartedAt: null,
    awaitingAck: false,
  };
}

function mapSessionUsageSummary(
  summary: SessionUsageSummaryWire | null | undefined,
): DesktopSessionUsageRecord | null {
  if (!summary || typeof summary !== "object") {
    return null;
  }
  const sessionId = String(summary.session_id ?? "").trim();
  const contextWindow =
    typeof summary.context_window === "number" ? summary.context_window : 0;
  const totalTokens =
    typeof summary.total_tokens === "number" ? summary.total_tokens : 0;
  const remainingTokens =
    typeof summary.remaining_tokens === "number" ? summary.remaining_tokens : 0;
  const updatedAt =
    typeof summary.updated_at === "number" ? summary.updated_at : 0;
  if (
    !sessionId &&
    contextWindow <= 0 &&
    totalTokens <= 0 &&
    remainingTokens <= 0 &&
    updatedAt <= 0
  ) {
    return null;
  }
  return {
    sessionId,
    provider: String(summary.provider ?? "").trim(),
    model: String(summary.model ?? "").trim(),
    source: String(summary.source ?? "").trim(),
    contextWindow,
    totalTokens,
    remainingTokens,
    updatedAt,
  };
}

function mapSessionCodexConfig(
  config: SessionCodexConfigWire | null | undefined,
): DesktopSessionCodexConfig {
  return {
    sessionId: String(config?.session_id ?? "").trim(),
    provider: String(config?.provider ?? "").trim(),
    model: String(config?.model ?? "").trim(),
    thinking: String(config?.thinking ?? "").trim(),
    serviceTier: String(config?.service_tier ?? "").trim(),
    contextMode: String(config?.context_mode ?? "").trim(),
    effectiveContextWindow:
      typeof config?.effective_context_window === "number"
        ? config.effective_context_window
        : 0,
    updatedAt: typeof config?.updated_at === "number" ? config.updated_at : 0,
  };
}

function mapSessionPlanDocument(
  document: SessionPlanDocumentWire | null | undefined,
): DesktopSessionPlanDocument | null {
  if (!document) {
    return null;
  }
  const info = document.info ?? {};
  return {
    id: String(document.id ?? "").trim(),
    title: String(document.title ?? "").trim(),
    status: String(document.status ?? "").trim(),
    schemaVersion: String(document.schema_version ?? "").trim(),
    revisionId: String(document.revision_id ?? "").trim(),
    info: {
      goal: String(info.goal ?? "").trim(),
      scope: String(info.scope ?? info.context ?? "").trim(),
      context: String(info.context ?? "").trim(),
      decisions: mapStringArray(info.decisions),
      constraints: mapStringArray(info.constraints),
      assumptions: mapStringArray(info.assumptions),
      openQuestions: mapStringArray(info.open_questions),
      relevantFiles: mapStringArray(info.relevant_files ?? info.files),
      successCriteria: mapStringArray(info.success_criteria),
      validationStrategy: Array.isArray(info.validation)
        ? mapStringArray(info.validation).join("; ")
        : String(info.validation_strategy ?? info.validation ?? "").trim(),
    },
    checkpoints: Array.isArray(document.checkpoints)
      ? document.checkpoints.map((checkpoint, index) => ({
          id: String(checkpoint?.id ?? "").trim(),
          title: String(checkpoint?.title ?? "").trim(),
          status: String(checkpoint?.status ?? "").trim(),
          objective: String(checkpoint?.objective ?? "").trim(),
          tasks: mapStringArray(checkpoint?.tasks),
          acceptanceCriteria: mapStringArray(checkpoint?.acceptance_criteria),
          notes: String(checkpoint?.notes ?? "").trim(),
          report: String(checkpoint?.report ?? "").trim(),
          result: String(checkpoint?.result ?? "").trim(),
          changedFiles: mapStringArray(checkpoint?.changed_files),
          validation: mapStringArray(checkpoint?.validation),
          order: typeof checkpoint?.order === "number" ? checkpoint.order : index + 1,
        }))
      : [],
    activeCheckpointId: String(document.active_checkpoint_id ?? "").trim(),
    renderedText: String(document.rendered_text ?? ""),
    displayText: String(document.display_text ?? ""),
  };
}

function mapSessionPlan(
  plan: SessionPlanWire | null | undefined,
): DesktopSessionPlanRecord {
  return {
    id: String(plan?.id ?? "").trim(),
    title: String(plan?.title ?? "").trim(),
    plan: String(plan?.plan ?? ""),
    document: mapSessionPlanDocument(plan?.document),
    status: String(plan?.status ?? "").trim(),
    approvalState: String(plan?.approval_state ?? "").trim(),
    updatedAt: typeof plan?.updated_at === "number" ? plan.updated_at : 0,
  };
}

function mapSessionPlanRevision(
  plan: SessionPlanWire | null | undefined,
  index: number,
): DesktopSessionPlanRevisionRecord {
  const base = mapSessionPlan(plan);
  const version = typeof plan?.version === "number" ? plan.version : 0;
  return {
    ...base,
    key: `${base.id || "plan"}:${version}:${index}`,
    createdAt: typeof plan?.created_at === "number" ? plan.created_at : 0,
    priorTitle: String(plan?.prior_title ?? ""),
    priorPlan: String(plan?.prior_plan ?? ""),
    diffLines: Array.isArray(plan?.diff_lines)
      ? plan.diff_lines.map((line) => String(line))
      : [],
    updateSummary: String(plan?.update_summary ?? "").trim(),
    updateScope: String(plan?.update_scope ?? "").trim(),
    updateKind: String(plan?.update_kind ?? "").trim(),
    version,
    parentRevision:
      typeof plan?.parent_revision === "number" ? plan.parent_revision : 0,
    checkpoint: Boolean(plan?.checkpoint),
  };
}

function routeFromSessionMetadata(session: DesktopSessionRecord): DesktopChatRoute | null {
  return desktopChatRouteFromSessionMetadata(session);
}

export function mapDesktopSession(session: unknown): DesktopSessionRecord {
  return mapSession(session as SessionWire);
}

function mapSessionProjectionToSession(session: SessionWire, projection: SessionProjectionWire | null | undefined): SessionWire {
  if (!projection || typeof projection !== "object") {
    return session;
  }
  return {
    ...session,
    session_api: String(session.session_api ?? "").trim() || "v3",
    last_event_seq:
      typeof projection.last_event_seq === "number"
        ? projection.last_event_seq
        : session.last_event_seq,
    projection_high_watermark_seq:
      typeof projection.projection_high_watermark_seq === "number"
        ? projection.projection_high_watermark_seq
        : session.projection_high_watermark_seq,
  };
}

function applySessionProjectionCursor(session: DesktopSessionRecord, projection: SessionProjectionWire | null | undefined): DesktopSessionRecord {
  if (!projection || typeof projection !== "object") {
    return session;
  }
  return {
    ...session,
    sessionApi: session.sessionApi || "v3",
    lastEventSeq:
      typeof projection.last_event_seq === "number"
        ? projection.last_event_seq
        : (session.lastEventSeq ?? 0),
    projectionHighWatermarkSeq:
      typeof projection.projection_high_watermark_seq === "number"
        ? projection.projection_high_watermark_seq
        : (session.projectionHighWatermarkSeq ?? 0),
  };
}

function mapChatMessage(message: MessageWire): ChatMessageRecord {
  const content = String(message.content ?? "");
  return {
    id: String(message.id ?? "").trim(),
    sessionId: String(message.session_id ?? "").trim(),
    globalSeq: typeof message.global_seq === "number" ? message.global_seq : 0,
    role: String(message.role ?? "").trim(),
    content,
    createdAt: typeof message.created_at === "number" ? message.created_at : 0,
    metadata: message.metadata,
    toolMessage: parseStructuredToolMessage(content),
  };
}

function mapV3RunIntent(intent: V3RunIntentWire | null | undefined): V3RunIntentRecord | null {
  if (!intent || typeof intent !== "object") {
    return null;
  }
  return {
    sessionId: String(intent.session_id ?? "").trim(),
    runId: String(intent.run_id ?? "").trim(),
    status: String(intent.status ?? "").trim(),
    blockedReason: String(intent.blocked_reason ?? "").trim(),
    createdAt: typeof intent.created_at === "number" ? intent.created_at : 0,
    updatedAt: typeof intent.updated_at === "number" ? intent.updated_at : 0,
    eventSeq: typeof intent.event_seq === "number" ? intent.event_seq : 0,
  };
}

function mapV3MessageCommitResponse(response: V3MessageCommitResponseWire): SendSessionMessageResult {
  const mappedSession = mapSession(mapSessionProjectionToSession(response.session ?? {}, response.projection));
  const session = mappedSession.id ? applySessionProjectionCursor(mappedSession, response.projection) : undefined;
  return {
    ok: response.ok,
    session,
    message: response.message ? mapChatMessage(response.message) : null,
    messages: Array.isArray(response.messages) ? response.messages.map(mapChatMessage) : [],
    runIntent: mapV3RunIntent(response.run_intent),
    events: Array.isArray(response.events) ? response.events : [],
  };
}

function mapSession(session: SessionWire): DesktopSessionRecord {
  const lifecycle =
    session.lifecycle && typeof session.lifecycle === "object"
      ? {
          sessionId: String(
            session.lifecycle.session_id ?? session.id ?? "",
          ).trim(),
          runId: String(session.lifecycle.run_id ?? "").trim() || null,
          active: Boolean(session.lifecycle.active),
          phase: String(session.lifecycle.phase ?? "").trim(),
          startedAt:
            typeof session.lifecycle.started_at === "number"
              ? session.lifecycle.started_at
              : 0,
          endedAt:
            typeof session.lifecycle.ended_at === "number"
              ? session.lifecycle.ended_at
              : 0,
          updatedAt:
            typeof session.lifecycle.updated_at === "number"
              ? session.lifecycle.updated_at
              : 0,
          generation:
            typeof session.lifecycle.generation === "number"
              ? session.lifecycle.generation
              : 0,
          stopReason:
            String(session.lifecycle.stop_reason ?? "").trim() || null,
          error: String(session.lifecycle.error ?? "").trim() || null,
          ownerTransport:
            String(session.lifecycle.owner_transport ?? "").trim() || null,
        }
      : null;
  const normalizedLifecyclePhase = lifecycle?.phase.trim().toLowerCase() ?? "";
  const liveStatus = lifecycle?.active
    ? ((["starting", "running", "blocked"].includes(normalizedLifecyclePhase)
        ? normalizedLifecyclePhase
        : "running") as DesktopSessionRecord["live"]["status"])
    : normalizedLifecyclePhase === "errored"
      ? "error"
      : "idle";
  const metadata =
    session.metadata && typeof session.metadata === "object"
      ? (session.metadata as Record<string, unknown>)
      : undefined;
  const workspacePath = String(session.workspace_path ?? "").trim();
  const workspaceFacts = sessionWorkspaceFactsFromMetadata(metadata);
  const worktreeEnabled = workspaceFacts.worktreeEnabled ?? Boolean(session.worktree_enabled);
  const worktreeRootPath = String(session.worktree_root_path ?? workspaceFacts.worktreeRootPath).trim();
  const canonicalWorkspacePath = canonicalSessionWorkspacePath({
    workspacePath,
    sourceWorkspacePath: workspaceFacts.sourceWorkspacePath,
    runtimeWorkspacePath: workspaceFacts.runtimeWorkspacePath,
    worktreeEnabled,
    worktreeRootPath,
  });
  return {
    id: String(session.id ?? "").trim(),
    title: String(session.title ?? "").trim(),
    workspacePath: canonicalWorkspacePath,
    workspaceName: canonicalSessionWorkspaceName(
      String(session.workspace_name ?? ""),
      workspacePath,
      canonicalWorkspacePath,
    ),
    mode: String(session.mode ?? "auto").trim() || "auto",
    metadata,
    sessionApi: String(session.session_api ?? "").trim(),
    lastEventSeq:
      typeof session.last_event_seq === "number" ? session.last_event_seq : 0,
    projectionHighWatermarkSeq:
      typeof session.projection_high_watermark_seq === "number" ? session.projection_high_watermark_seq : 0,
    messageCount:
      typeof session.message_count === "number" ? session.message_count : 0,
    updatedAt: typeof session.updated_at === "number" ? session.updated_at : 0,
    createdAt: typeof session.created_at === "number" ? session.created_at : 0,
    permissionsHydrated: true,
    runtimeWorkspacePath: workspaceFacts.runtimeWorkspacePath || workspacePath,
    worktreeEnabled,
    worktreeRootPath,
    worktreeBaseBranch: String(session.worktree_base_branch ?? "").trim(),
    worktreeBranch: String(session.worktree_branch ?? "").trim(),
    gitBranch: String(session.git_branch ?? "").trim(),
    gitHasGit: Boolean(session.git_has_git),
    gitClean: Boolean(session.git_clean),
    gitDirtyCount:
      typeof session.git_dirty_count === "number" ? session.git_dirty_count : 0,
    gitStagedCount:
      typeof session.git_staged_count === "number"
        ? session.git_staged_count
        : 0,
    gitModifiedCount:
      typeof session.git_modified_count === "number"
        ? session.git_modified_count
        : 0,
    gitUntrackedCount:
      typeof session.git_untracked_count === "number"
        ? session.git_untracked_count
        : 0,
    gitConflictCount:
      typeof session.git_conflict_count === "number"
        ? session.git_conflict_count
        : 0,
    gitAheadCount:
      typeof session.git_ahead_count === "number" ? session.git_ahead_count : 0,
    gitBehindCount:
      typeof session.git_behind_count === "number"
        ? session.git_behind_count
        : 0,
    gitCommitDetected: Boolean(session.git_commit_detected),
    gitCommitCount:
      typeof session.git_commit_count === "number"
        ? session.git_commit_count
        : 0,
    gitCommittedFileCount:
      typeof session.git_committed_file_count === "number"
        ? session.git_committed_file_count
        : 0,
    gitCommittedAdditions:
      typeof session.git_committed_additions === "number"
        ? session.git_committed_additions
        : 0,
    gitCommittedDeletions:
      typeof session.git_committed_deletions === "number"
        ? session.git_committed_deletions
        : 0,
    lifecycle,
    live: {
      ...emptyLiveState(),
      runId: lifecycle?.active ? lifecycle.runId : null,
      startedAt:
        lifecycle?.active && lifecycle.startedAt > 0
          ? lifecycle.startedAt
          : null,
      status: liveStatus,
      lastEventAt: lifecycle?.updatedAt ? lifecycle.updatedAt : null,
      summary: lifecycle?.active
        ? null
        : normalizedLifecyclePhase === "errored"
          ? (lifecycle?.error ?? lifecycle?.stopReason ?? null)
          : (lifecycle?.stopReason ?? null),
      error:
        normalizedLifecyclePhase === "errored"
          ? (lifecycle?.error ?? lifecycle?.stopReason ?? null)
          : null,
    },
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
  };
}

export async function fetchSession(
  sessionId: string,
  options: SessionDataRequestOptions = {},
): Promise<DesktopSessionRecord | null> {
  const normalizedSessionId = sessionId.trim();
  if (!normalizedSessionId) {
    return null;
  }

  const sessionApi = resolveSessionApiForSession(normalizedSessionId, options);
  if (sessionApi === "v3") {
    const response = await requestJson<V3HydratedSessionResponseWire>(
      `/v3/sessions/${encodeURIComponent(normalizedSessionId)}`,
    );
    const mappedSession = mapSession(mapSessionProjectionToSession(response.session ?? {}, response.projection));
    const mapped = applyDesktopChatRouteToSession(
      mappedSession,
      routeFromSessionMetadata(mappedSession),
    );
    mapped.permissionsHydrated = false;
    return mapped.id ? applySessionProjectionCursor(mapped, response.projection) : null;
  }

  const response = await requestJson<{ session?: SessionWire }>(
    `/v2/sessions/${encodeURIComponent(normalizedSessionId)}`,
  );
  const mappedSession = mapSession(response.session ?? {});
  const mapped = applyDesktopChatRouteToSession(
    mappedSession,
    routeFromSessionMetadata(mappedSession),
  );
  mapped.permissionsHydrated = false;
  return mapped.id ? mapped : null;
}

function mapResolvedPermission(
  permission: ResolvePermissionResponseWire["permission"],
  savedRule?: ResolvePermissionResponseWire["saved_rule"],
): DesktopPermissionRecord {
  return {
    id: String(permission?.id ?? "").trim(),
    sessionId: String(permission?.session_id ?? "").trim(),
    runId: String(permission?.run_id ?? "").trim(),
    callId: String(permission?.call_id ?? "").trim(),
    toolName: String(permission?.tool_name ?? "").trim(),
    toolArguments: String(permission?.tool_arguments ?? "").trim(),
    approvedArguments:
      String(
        (permission as { approved_arguments?: unknown } | undefined)
          ?.approved_arguments ?? "",
      ).trim() || undefined,
    savedRule: savedRule
      ? {
          id: String(savedRule.id ?? "").trim(),
          kind: String(savedRule.kind ?? "").trim(),
          decision: String(savedRule.decision ?? "").trim(),
          tool:
            typeof savedRule.tool === "string"
              ? savedRule.tool.trim()
              : undefined,
          pattern:
            typeof savedRule.pattern === "string"
              ? savedRule.pattern.trim()
              : undefined,
          createdAt:
            typeof savedRule.created_at === "number"
              ? savedRule.created_at
              : undefined,
          updatedAt:
            typeof savedRule.updated_at === "number"
              ? savedRule.updated_at
              : undefined,
        }
      : undefined,
    status: String(permission?.status ?? "").trim(),
    decision: String(permission?.decision ?? "").trim(),
    reason: String(permission?.reason ?? "").trim(),
    requirement: String(permission?.requirement ?? "").trim(),
    mode: String(permission?.mode ?? "").trim(),
    createdAt:
      typeof permission?.created_at === "number" ? permission.created_at : 0,
    updatedAt:
      typeof permission?.updated_at === "number" ? permission.updated_at : 0,
    resolvedAt:
      typeof permission?.resolved_at === "number" ? permission.resolved_at : 0,
    permissionRequestedAt:
      typeof permission?.permission_requested_at === "number"
        ? permission.permission_requested_at
        : 0,
  };
}

export async function fetchSessionMessages(
  sessionId: string,
  signal?: AbortSignal,
  afterSeq = 0,
  options: SessionDataRequestOptions = {},
): Promise<ChatMessageRecord[]> {
  const normalizedSessionId = sessionId.trim();
  const search = new URLSearchParams({ limit: "100" });
  if (afterSeq > 0) {
    search.set("after_seq", String(afterSeq));
  }
  const sessionApi = resolveSessionApiForSession(normalizedSessionId, options);
  const endpoint = sessionApi === "v3"
    ? `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/messages?${search.toString()}`
    : `/v2/sessions/${encodeURIComponent(normalizedSessionId)}/messages?${search.toString()}`;
  const response = await requestJson<MessagesResponseWire>(
    endpoint,
    { signal },
  );
  return Array.isArray(response.messages)
    ? response.messages.map(mapChatMessage)
    : [];
}

export async function fetchSessionPreference(
  sessionId: string,
  signal?: AbortSignal,
): Promise<ResolvedSessionPreference> {
  rejectV3SessionV2Subresource(sessionId, "session preference");
  const response = await requestJson<SessionPreferenceWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/preference`,
    { signal },
  );
  return {
    preference: {
      provider: String(response.preference?.provider ?? "").trim(),
      model: String(response.preference?.model ?? "").trim(),
      thinking: String(response.preference?.thinking ?? "").trim(),
      serviceTier: String(response.preference?.service_tier ?? "").trim(),
      contextMode: String(response.preference?.context_mode ?? "").trim(),
      updatedAt:
        typeof response.preference?.updated_at === "number"
          ? response.preference.updated_at
          : 0,
    },
    contextWindow:
      typeof response.context_window === "number" ? response.context_window : 0,
    maxOutputTokens:
      typeof response.max_output_tokens === "number"
        ? response.max_output_tokens
        : 0,
  };
}

export async function fetchSessionMode(
  sessionId: string,
  signal?: AbortSignal,
): Promise<string> {
  rejectV3SessionV2Subresource(sessionId, "session mode");
  const response = await requestJson<{ mode?: string }>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/mode`,
    { signal },
  );
  return String(response.mode ?? "").trim() || "auto";
}

export async function updateSessionMode(
  sessionId: string,
  mode: string,
): Promise<string> {
  rejectV3SessionV2Subresource(sessionId, "session mode");
  const response = await requestJson<{ mode?: string }>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/mode`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ mode }),
    },
  );
  return String(response.mode ?? "").trim() || "auto";
}

export async function fetchSessionMetadata(
  sessionId: string,
  signal?: AbortSignal,
): Promise<Record<string, unknown>> {
  rejectV3SessionV2Subresource(sessionId, "session metadata");
  const response = await requestJson<SessionMetadataWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/metadata`,
    { signal },
  );
  return response.metadata && typeof response.metadata === "object"
    ? response.metadata
    : {};
}

export async function updateSessionMetadata(
  sessionId: string,
  metadata: Record<string, unknown>,
  route?: DesktopChatRoute | null,
): Promise<DesktopSessionRecord> {
  rejectV3SessionV2Subresource(sessionId, "session metadata");
  const response = await requestJson<{ session?: SessionWire }>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/metadata`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        metadata: sanitizeSessionCreateV2Metadata(metadata) ?? {},
      }),
    },
  );
  const mappedSession = mapSession(response.session ?? {});
  return applyDesktopChatRouteToSession(
    mappedSession,
    route ?? routeFromSessionMetadata(mappedSession),
  );
}

export async function fetchSessionCodexConfig(
  sessionId: string,
  signal?: AbortSignal,
): Promise<DesktopSessionCodexConfig> {
  rejectV3SessionV2Subresource(sessionId, "session Codex config");
  const response = await requestJson<SessionCodexConfigWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/codex`,
    { signal },
  );
  return mapSessionCodexConfig(response);
}

export async function updateSessionCodexConfig(
  sessionId: string,
  input: { serviceTier?: string; contextMode?: string },
): Promise<DesktopSessionCodexConfig> {
  rejectV3SessionV2Subresource(sessionId, "session Codex config");
  const response = await requestJson<SessionCodexConfigWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/codex`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        service_tier: input.serviceTier,
        context_mode: input.contextMode,
      }),
    },
  );
  return mapSessionCodexConfig(response);
}

export async function updateSessionPreference(
  sessionId: string,
  input: Partial<ResolvedSessionPreference["preference"]>,
): Promise<ResolvedSessionPreference> {
  rejectV3SessionV2Subresource(sessionId, "session preference");
  const response = await requestJson<SessionPreferenceWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/preference`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        provider: input.provider,
        model: input.model,
        thinking: input.thinking,
        service_tier: input.serviceTier,
        context_mode: input.contextMode,
      }),
    },
  );
  return {
    preference: {
      provider: String(response.preference?.provider ?? "").trim(),
      model: String(response.preference?.model ?? "").trim(),
      thinking: String(response.preference?.thinking ?? "").trim(),
      serviceTier: String(response.preference?.service_tier ?? "").trim(),
      contextMode: String(response.preference?.context_mode ?? "").trim(),
      updatedAt:
        typeof response.preference?.updated_at === "number"
          ? response.preference.updated_at
          : Date.now(),
    },
    contextWindow:
      typeof response.context_window === "number" ? response.context_window : 0,
    maxOutputTokens:
      typeof response.max_output_tokens === "number"
        ? response.max_output_tokens
        : 0,
  };
}

export async function fetchDraftModelPreference(
  signal?: AbortSignal,
): Promise<ResolvedSessionPreference> {
  const response = await requestJson<DraftModelWire>("/v1/model", { signal });
  return {
    preference: {
      provider: String(response.preference?.provider ?? "").trim(),
      model: String(response.preference?.model ?? "").trim(),
      thinking: String(response.preference?.thinking ?? "").trim(),
      serviceTier: String(response.preference?.service_tier ?? "").trim(),
      contextMode: String(response.preference?.context_mode ?? "").trim(),
      updatedAt:
        typeof response.preference?.updated_at === "number"
          ? response.preference.updated_at
          : 0,
    },
    contextWindow:
      typeof response.context_window === "number" ? response.context_window : 0,
    maxOutputTokens:
      typeof response.max_output_tokens === "number"
        ? response.max_output_tokens
        : 0,
  };
}

export async function updateDraftModelPreference(
  input: Partial<ResolvedSessionPreference["preference"]>,
): Promise<ResolvedSessionPreference> {
  const response = await requestJson<DraftModelWire>("/v1/model", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      provider: input.provider,
      model: input.model,
      thinking: input.thinking,
      service_tier: input.serviceTier,
      context_mode: input.contextMode,
    }),
  });
  return {
    preference: {
      provider: String(response.preference?.provider ?? "").trim(),
      model: String(response.preference?.model ?? "").trim(),
      thinking: String(response.preference?.thinking ?? "").trim(),
      serviceTier: String(response.preference?.service_tier ?? "").trim(),
      contextMode: String(response.preference?.context_mode ?? "").trim(),
      updatedAt:
        typeof response.preference?.updated_at === "number"
          ? response.preference.updated_at
          : Date.now(),
    },
    contextWindow:
      typeof response.context_window === "number" ? response.context_window : 0,
    maxOutputTokens:
      typeof response.max_output_tokens === "number"
        ? response.max_output_tokens
        : 0,
  };
}

function mapStringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.map((entry) => String(entry).trim()).filter(Boolean)
    : [];
}

function mapAgentToolInventory(
  inventory?: AgentToolInventoryWire | null,
): AgentToolInventoryRecord | null {
  if (!inventory || typeof inventory !== "object") {
    return null;
  }
  const tools = Array.isArray(inventory.tools)
    ? inventory.tools
        .map((tool) => {
          const name = String(tool.name ?? "").trim();
          const contractName = String(tool.contract_name ?? name).trim();
          if (!contractName) {
            return null;
          }
          return {
            name: name || contractName,
            contractName,
            description: String(tool.description ?? "").trim(),
            group: String(tool.group ?? "other").trim() || "other",
            kind: String(tool.kind ?? "built_in").trim() || "built_in",
          };
        })
        .filter((tool): tool is AgentToolInventoryRecord["tools"][number] => tool !== null)
    : [];
  const presets = Array.isArray(inventory.presets)
    ? inventory.presets
        .map((preset) => {
          const id = String(preset.id ?? "").trim();
          if (!id) {
            return null;
          }
          return {
            id,
            label: String(preset.label ?? id).trim() || id,
            description: String(preset.description ?? "").trim(),
            enabledTools: mapStringArray(preset.enabled_tools),
            disabledByDefault: mapStringArray(preset.disabled_by_default),
            bashPrefixes: mapStringArray(preset.bash_prefixes),
          };
        })
        .filter((preset): preset is AgentToolInventoryRecord["presets"][number] => preset !== null)
    : [];
  return { tools, presets };
}

function mapAgentToolContract(
  contract?: AgentToolContractWire | null,
): AgentToolContractRecord | null {
  if (!contract || typeof contract !== "object") {
    return null;
  }
  const tools: AgentToolContractRecord["tools"] = {};
  if (contract.tools && typeof contract.tools === "object") {
    for (const [name, config] of Object.entries(contract.tools)) {
      const toolName = name.trim();
      if (!toolName || !config || typeof config !== "object") {
        continue;
      }
      const enabled =
        typeof config.enabled === "boolean" ? config.enabled : undefined;
      tools[toolName] = {
        ...(enabled === undefined ? {} : { enabled }),
        bashPrefixes: mapStringArray(config.bash_prefixes),
      };
    }
  }
  return {
    preset: String(contract.preset ?? "").trim(),
    inheritPolicy: Boolean(contract.inherit_policy),
    tools,
  };
}

function mapResolvedAgentToolContract(
  resolved?: ResolvedAgentToolContractWire | null,
): ResolvedAgentToolContractRecord | null {
  if (!resolved || typeof resolved !== "object") {
    return null;
  }
  const tools: ResolvedAgentToolContractRecord["tools"] = {};
  if (resolved.tools && typeof resolved.tools === "object") {
    for (const [name, config] of Object.entries(resolved.tools)) {
      const toolName = name.trim();
      if (!toolName || !config || typeof config !== "object") {
        continue;
      }
      tools[toolName] = {
        enabled: Boolean(config.enabled),
        bashPrefixes: mapStringArray(config.bash_prefixes),
        source: String(config.source ?? "").trim(),
      };
    }
  }
  return {
    runtimeMode: String(resolved.runtime_mode ?? "").trim(),
    rawPreset: String(resolved.raw_preset ?? "").trim(),
    inheritPolicy: Boolean(resolved.inherit_policy),
    availableTools: mapStringArray(resolved.available_tools),
    unavailableTools: mapStringArray(resolved.unavailable_tools),
    tools,
  };
}

export async function fetchAgentState(
  signal?: AbortSignal,
): Promise<AgentStateRecord> {
  const response = await requestJson<AgentStateWire>("/v2/agents?limit=200", {
    signal,
  });
  return {
    profiles: Array.isArray(response.state?.profiles)
      ? response.state.profiles.map((profile) => ({
          name: String(profile.name ?? "").trim(),
          mode: String(profile.mode ?? "").trim(),
          description: String(profile.description ?? "").trim(),
          provider: String(profile.provider ?? "").trim(),
          model: String(profile.model ?? "").trim(),
          thinking: String(profile.thinking ?? "").trim(),
          prompt: String(profile.prompt ?? ""),
          runtimeMode: (() => {
            const raw = String(profile.runtime_mode ?? "")
              .trim()
              .toLowerCase();
            return raw === "plan_auto" || raw === "read" || raw === "readwrite"
              ? raw
              : "";
          })() as "plan_auto" | "read" | "readwrite" | "",
          executionSetting: (() => {
            const raw = String(profile.execution_setting ?? "")
              .trim()
              .toLowerCase();
            return raw === "read" || raw === "readwrite" ? raw : "";
          })() as "read" | "readwrite" | "",
          exitPlanModeEnabled: Boolean(profile.exit_plan_mode_enabled),
          toolScope: (() => {
            if (profile.tool_scope && typeof profile.tool_scope === "object") {
              return {
                preset: String(profile.tool_scope.preset ?? "").trim(),
                allowTools: Array.isArray(profile.tool_scope.allow_tools)
                  ? profile.tool_scope.allow_tools
                      .map((value) => String(value).trim())
                      .filter(Boolean)
                  : [],
                denyTools: Array.isArray(profile.tool_scope.deny_tools)
                  ? profile.tool_scope.deny_tools
                      .map((value) => String(value).trim())
                      .filter(Boolean)
                  : [],
                bashPrefixes: Array.isArray(profile.tool_scope.bash_prefixes)
                  ? profile.tool_scope.bash_prefixes
                      .map((value) => String(value).trim())
                      .filter(Boolean)
                  : [],
                inheritPolicy: Boolean(profile.tool_scope.inherit_policy),
              };
            }
            return null;
          })(),
          toolContract: mapAgentToolContract(profile.tool_contract),
          enabled: Boolean(profile.enabled),
          protected: Boolean((profile as { protected?: boolean }).protected),
          updatedAt:
            typeof profile.updated_at === "number" ? profile.updated_at : 0,
        }))
      : [],
    activePrimary: String(response.state?.active_primary ?? "").trim(),
    activeSubagent: response.state?.active_subagent ?? {},
    version:
      typeof response.state?.version === "number" ? response.state.version : 0,
    providerDefaultsPreview: mapProviderDefaultsPreview(
      response.provider_defaults_preview,
    ),
    toolInventory: mapAgentToolInventory(response.tool_inventory),
  };
}

export async function fetchAgentToolContract(
  name: string,
  signal?: AbortSignal,
): Promise<AgentToolContractRuntimeRecord> {
  const response = await requestJson<AgentToolContractResponseWire>(
    `/v2/agents/${encodeURIComponent(name.trim())}/tool-contract`,
    { signal },
  );
  return {
    agent: String(response.agent ?? name).trim(),
    rawToolContract: mapAgentToolContract(response.raw_tool_contract),
    resolved: mapResolvedAgentToolContract(response.resolved),
    compiledPolicy: response.compiled_policy,
    toolInventory: mapAgentToolInventory(response.tool_inventory),
  };
}

function mapProviderDefaultsPreview(
  preview?: ProviderDefaultsPreviewWire | null,
): ProviderDefaultsPreviewRecord | null {
  if (!preview || typeof preview !== "object") {
    return null;
  }
  return {
    provider: String(preview.provider ?? "").trim(),
    primaryAgent: String(preview.primary_agent ?? "").trim(),
    primaryModel: String(preview.primary_model ?? "").trim(),
    primaryThinking: String(preview.primary_thinking ?? "").trim(),
    utilityProvider: String(preview.utility_provider ?? preview.provider ?? "").trim(),
    utilityModel: String(preview.utility_model ?? "").trim(),
    utilityThinking: String(preview.utility_thinking ?? "").trim(),
    utilityAgents: Array.isArray(preview.utility_agents)
      ? preview.utility_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    affectedAgents: Array.isArray(preview.affected_agents)
      ? preview.affected_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    outOfSyncAgents: Array.isArray(preview.out_of_sync_agents)
      ? preview.out_of_sync_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    inheritingAgents: Array.isArray(preview.inheriting_agents)
      ? preview.inheriting_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    staleInheritedAgents: Array.isArray(preview.stale_inherited_agents)
      ? preview.stale_inherited_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    customUtilityAgents: Array.isArray(preview.custom_utility_agents)
      ? preview.custom_utility_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    utilityBaselineAgents: Array.isArray(preview.utility_baseline_agents)
      ? preview.utility_baseline_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    overwriteExplicit: Boolean(preview.overwrite_explicit),
  };
}

function mapAgentDefaultsState(
  response: RestoreAgentDefaultsWire,
): AgentStateRecord {
  const state = {
    profiles: response.profiles,
    active_primary: response.active_primary,
    active_subagent: response.active_subagent,
    version: response.version,
  };
  return {
    profiles: Array.isArray(state?.profiles)
      ? state.profiles.map((profile) => ({
          name: String(profile.name ?? "").trim(),
          mode: String(profile.mode ?? "").trim(),
          description: String(profile.description ?? "").trim(),
          provider: String(profile.provider ?? "").trim(),
          model: String(profile.model ?? "").trim(),
          thinking: String(profile.thinking ?? "").trim(),
          prompt: String(profile.prompt ?? ""),
          runtimeMode: (() => {
            const raw = String(profile.runtime_mode ?? "")
              .trim()
              .toLowerCase();
            return raw === "plan_auto" || raw === "read" || raw === "readwrite"
              ? raw
              : "";
          })() as "plan_auto" | "read" | "readwrite" | "",
          executionSetting: (() => {
            const raw = String(profile.execution_setting ?? "")
              .trim()
              .toLowerCase();
            return raw === "read" || raw === "readwrite" ? raw : "";
          })() as "read" | "readwrite" | "",
          exitPlanModeEnabled: Boolean(profile.exit_plan_mode_enabled),
          toolScope: null,
          toolContract: null,
          enabled: Boolean(profile.enabled),
          protected: Boolean((profile as { protected?: boolean }).protected),
          updatedAt:
            typeof profile.updated_at === "number" ? profile.updated_at : 0,
        }))
      : [],
    activePrimary: String(state?.active_primary ?? "").trim(),
    activeSubagent: state?.active_subagent ?? {},
    version: typeof state?.version === "number" ? state.version : 0,
    providerDefaultsPreview: mapProviderDefaultsPreview(
      response.provider_defaults_preview,
    ),
    toolInventory: null,
  };
}

export async function restoreAgentDefaults(input?: {
  utilityProvider?: string;
  utilityModel?: string;
  utilityThinking?: string;
  overwriteExplicit?: boolean;
}): Promise<AgentStateRecord> {
  const body: Record<string, string | boolean> = {};
  if (input?.utilityProvider !== undefined) {
    body.utility_provider = input.utilityProvider;
  }
  if (input?.utilityModel !== undefined) {
    body.utility_model = input.utilityModel;
  }
  if (input?.utilityThinking !== undefined) {
    body.utility_thinking = input.utilityThinking;
  }
  if (input?.overwriteExplicit !== undefined) {
    body.overwrite_explicit = input.overwriteExplicit;
  }
  const response = await requestJson<RestoreAgentDefaultsWire>(
    "/v2/agents/defaults/restore",
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
    },
  );
  return mapAgentDefaultsState(response);
}

export async function resetAgentDefaults(): Promise<AgentStateRecord> {
  const response = await requestJson<RestoreAgentDefaultsWire>(
    "/v2/agents/defaults/reset",
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({}),
    },
  );
  return mapAgentDefaultsState(response);
}

export async function activatePrimaryAgent(name: string): Promise<void> {
  await requestJson("/v2/agents/active/primary", {
    method: "PUT",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ name: name.trim() }),
  });
}

const SESSION_CREATE_V2_FORBIDDEN_METADATA_KEYS = new Set([
  'workspace_name',
  'workspace_path',
  'host_workspace_path',
  'runtime_workspace_path',
  'backend_url',
  'child_backend_url',
  'target_backend_url',
  'target_swarm_id',
  'next_hop_swarm_id',
  'next_hop_backend_url',
  'local_workspace_binding_id',
  'owner_transport',
])

const SESSION_CREATE_V2_FORBIDDEN_METADATA_PREFIXES = [
  'swarm_route_',
  'swarm_routed_',
  'swarm_managed_',
  'swarm_v2_',
  'hosted_session',
  'managed_host',
]

const SESSION_CREATE_V2_FORBIDDEN_METADATA_PARTS = [
  'workspace_name',
  'workspace_path',
  'path',
  'backend_url',
  'swarm_id',
  'route',
  'routing',
  'backend',
  'target',
]

function isForbiddenSessionCreateV2MetadataKey(key: string): boolean {
  const normalized = key.trim().toLowerCase()
  if (!normalized) {
    return true
  }
  if (SESSION_CREATE_V2_FORBIDDEN_METADATA_KEYS.has(normalized)) {
    return true
  }
  if (SESSION_CREATE_V2_FORBIDDEN_METADATA_PREFIXES.some((prefix) => normalized.startsWith(prefix))) {
    return true
  }
  return SESSION_CREATE_V2_FORBIDDEN_METADATA_PARTS.some((part) => normalized.includes(part))
}

function sanitizeSessionCreateV2MetadataValue(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map((child) => sanitizeSessionCreateV2MetadataValue(child))
  }
  if (!value || typeof value !== 'object') {
    return value
  }
  const sanitized: Record<string, unknown> = {}
  for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
    if (isForbiddenSessionCreateV2MetadataKey(key)) {
      continue
    }
    sanitized[key] = sanitizeSessionCreateV2MetadataValue(child)
  }
  return sanitized
}

export function sanitizeSessionCreateV2Metadata(metadata: Record<string, unknown> | null | undefined): Record<string, unknown> | undefined {
  if (!metadata || typeof metadata !== 'object') {
    return undefined
  }
  const sanitized = sanitizeSessionCreateV2MetadataValue(metadata) as Record<string, unknown>
  return Object.keys(sanitized).length > 0 ? sanitized : undefined
}

function optionalString(value: string | null | undefined): string | undefined {
  const normalized = value?.trim() ?? ''
  return normalized ? normalized : undefined
}

function stripUndefinedFields<T extends Record<string, unknown>>(value: T): T {
  for (const key of Object.keys(value)) {
    if (value[key] === undefined) {
      delete value[key]
    }
  }
  return value
}

function sessionCreatePreferenceBody(preferenceInput: ResolvedSessionPreference["preference"]): Record<string, unknown> {
  const preference = stripUndefinedFields({
    provider: optionalString(preferenceInput.provider),
    model: optionalString(preferenceInput.model),
    thinking: optionalString(preferenceInput.thinking),
    service_tier: optionalString(preferenceInput.serviceTier),
    context_mode: optionalString(preferenceInput.contextMode),
  })
  return preference
}

function sessionCreateV2RequestBody(input: {
  target: { swarmId: string; workspaceBindingId: string };
  title?: string;
  mode: string;
  agentName?: string;
  metadata?: Record<string, unknown>;
  preference: ResolvedSessionPreference["preference"];
  worktreeMode?: string;
  worktreeUseCurrentBranch?: boolean;
  worktreeBaseBranch?: string;
  worktreeBranchName?: string;
}): Record<string, unknown> {
  const worktreeMode = optionalString(input.worktreeMode) ?? "off"
  const preference = sessionCreatePreferenceBody(input.preference)
  return stripUndefinedFields({
    swarm_id: input.target.swarmId,
    workspace_binding_id: input.target.workspaceBindingId,
    title: input.title ?? "",
    mode: input.mode,
    agent_name: input.agentName?.trim() ?? "",
    worktree_mode: worktreeMode,
    worktree_use_current_branch: worktreeMode === "on" ? input.worktreeUseCurrentBranch : undefined,
    worktree_base_branch: worktreeMode === "on" ? optionalString(input.worktreeBaseBranch) : undefined,
    worktree_branch_name: worktreeMode === "on" ? optionalString(input.worktreeBranchName) : undefined,
    preference: Object.keys(preference).length > 0 ? preference : undefined,
    metadata: sanitizeSessionCreateV2Metadata(input.metadata),
  })
}

function sessionCreateV3RequestBody(input: {
  title?: string;
  workspacePath: string;
  workspaceName: string;
  mode: string;
  metadata?: Record<string, unknown>;
  preference: ResolvedSessionPreference["preference"];
}): Record<string, unknown> {
  const preference = sessionCreatePreferenceBody(input.preference)
  const title = optionalString(input.title)
  return stripUndefinedFields({
    client_request_id: `desktop-v3-create:${crypto.randomUUID()}`,
    title: title || undefined,
    workspace_path: input.workspacePath,
    workspace_name: input.workspaceName,
    mode: input.mode,
    preference: Object.keys(preference).length > 0 ? preference : undefined,
    metadata: sanitizeSessionCreateV2Metadata(input.metadata),
  })
}

export async function createSession(input: {
  title?: string;
  workspacePath: string;
  workspaceName: string;
  mode: string;
  agentName?: string;
  metadata?: Record<string, unknown>;
  preference: ResolvedSessionPreference["preference"];
  route?: DesktopChatRoute | null;
  worktreeMode?: string;
  worktreeUseCurrentBranch?: boolean;
  worktreeBaseBranch?: string;
  worktreeBranchName?: string;
}): Promise<DesktopSessionRecord> {
  const target = getDesktopSessionCreateV2Target(input.route)
  if (target.endpoint === null) {
    throw new Error(target.unsupportedReason)
  }
  const body = target.sessionApi === "v3"
    ? sessionCreateV3RequestBody({
      title: input.title,
      workspacePath: input.workspacePath,
      workspaceName: input.workspaceName,
      mode: input.mode,
      metadata: input.metadata,
      preference: input.preference,
    })
    : sessionCreateV2RequestBody({
      target: { swarmId: target.swarmId, workspaceBindingId: target.workspaceBindingId },
      title: input.title,
      mode: input.mode,
      agentName: input.agentName,
      metadata: input.metadata,
      preference: input.preference,
      worktreeMode: input.worktreeMode,
      worktreeUseCurrentBranch: input.worktreeUseCurrentBranch,
      worktreeBaseBranch: input.worktreeBaseBranch,
      worktreeBranchName: input.worktreeBranchName,
    })
  const response = await requestJson<V3HydratedSessionResponseWire & { session_execution?: Record<string, unknown> }>(
    target.endpoint,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
    },
  );
  const mapped = applyDesktopChatRouteToSession(
    mapSession(mapSessionProjectionToSession(response.session ?? {}, response.projection)),
    input.route,
  );
  return applySessionProjectionCursor(mapped, response.projection);
}

export async function sendSessionMessage(
  sessionId: string,
  role: "user" | "assistant" | "system" | "tool" | "reasoning",
  content: string,
  route?: DesktopChatRoute | null,
  options: SendSessionMessageOptions = {},
): Promise<SendSessionMessageResult | unknown> {
  const normalizedSessionId = sessionId.trim();
  const sessionApi = resolveSessionApiForSession(normalizedSessionId, options);
  if (sessionApi === "v3") {
    const clientRequestId = options.clientRequestId?.trim()
      || `desktop-v3-message:${normalizedSessionId}:${crypto.randomUUID()}`;
    const response = await requestJson<V3MessageCommitResponseWire>(
      `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/messages`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          client_request_id: clientRequestId,
          role,
          content,
        }),
      },
    );
    return mapV3MessageCommitResponse(response);
  }

  const managedHost = isManagedHostDesktopChatRoute(route)
  return requestJson(
    managedHost
      ? "/v1/swarm/managed-hosts/sessions/message"
      : `/v2/sessions/${encodeURIComponent(normalizedSessionId)}/messages`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(
        managedHost
          ? { target_swarm_id: route?.swarmId?.trim() ?? "", session_id: normalizedSessionId, role, content }
          : { role, content },
      ),
    },
  );
}

export interface DesktopBackgroundRunStartOptions {
  sessionId: string;
  route?: DesktopChatRoute | null;
  stream?: boolean;
  prompt: string;
  agentName?: string;
  instructions?: string;
  compact?: boolean;
  background?: boolean;
  targetKind?: string;
  targetName?: string;
  toolScope?: {
    preset?: string;
    allow_tools?: string[];
    deny_tools?: string[];
    bash_prefixes?: string[];
    inherit_policy?: boolean;
  };
  executionContext?: {
    workspace_path?: string;
    cwd?: string;
    worktree_mode?: string;
    worktree_root_path?: string;
    worktree_branch?: string;
    worktree_base_branch?: string;
  };
}

export interface DesktopRunAccepted {
  ok?: boolean;
  session_id?: string;
  run_id?: string;
  status?: string;
  background?: boolean;
  target_kind?: string;
  target_name?: string;
  owner_transport?: string;
}

export async function startSessionRun(
  options: DesktopBackgroundRunStartOptions,
): Promise<DesktopRunAccepted> {
  const sessionId = options.sessionId.trim();
  rejectV3SessionV2Subresource(sessionId, "session run dispatch");
  if (!sessionId) {
    throw new Error("session id is required");
  }
  const prompt = options.prompt.trim();
  if (!prompt && !options.compact) {
    throw new Error("prompt is required");
  }

  const managedHost = isManagedHostDesktopChatRoute(options.route);
  const primaryEndpoint = options.stream === false ? "run" : "run/stream";
  const effectiveAgentName = managedHost
    ? (options.agentName?.trim() ?? "")
    : (options.targetName?.trim() || options.agentName?.trim() || "");
  const nativePrimaryBody = {
    type: "run.start",
    prompt,
    agent_name: effectiveAgentName,
    instructions: options.instructions?.trim() ?? "",
    compact: Boolean(options.compact),
    background: Boolean(options.background),
  };
  const managedHostBody = {
    ...nativePrimaryBody,
    agent_name: options.agentName?.trim() ?? "",
    target_swarm_id: options.route?.swarmId?.trim() ?? "",
    session_id: sessionId,
    target_kind: options.targetKind?.trim() ?? "",
    target_name: options.targetName?.trim() ?? "",
    tool_scope: options.toolScope,
    execution_context: options.executionContext,
  };
  return requestJson<DesktopRunAccepted>(
    managedHost
      ? "/v1/swarm/managed-hosts/sessions/run"
      : `/v2/sessions/${encodeURIComponent(sessionId)}/${primaryEndpoint}`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(managedHost ? managedHostBody : nativePrimaryBody),
    },
  );
}

export async function openRunStream(
  sessionId: string,
  options: { sessionApi?: string | null; afterSeq?: number } = {},
): Promise<WebSocket> {
  const normalizedSessionId = sessionId.trim();
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  await ensureDesktopSession(true);
  const sessionApi = resolveSessionApiForSession(normalizedSessionId, options);
  const url = new URL(
    sessionApi === "v3"
      ? `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/stream`
      : `/v2/sessions/${encodeURIComponent(normalizedSessionId)}/run/stream`,
    `${protocol}//${window.location.host}`,
  );
  if (sessionApi === "v3" && typeof options.afterSeq === "number" && Number.isFinite(options.afterSeq) && options.afterSeq > 0) {
    url.searchParams.set("after_seq", String(Math.floor(options.afterSeq)));
  }
  return new WebSocket(url);
}

export async function stopSessionRun(
  sessionId: string,
  runId: string,
  route?: DesktopChatRoute | null,
): Promise<void> {
  rejectV3SessionV2Subresource(sessionId, "session run stop");
  const managedHost = isManagedHostDesktopChatRoute(route);
  const primaryTarget = isPrimaryDesktopChatRoute(route);
  const localContainerTarget = isLocalContainerDesktopChatRoute(route);
  if (!managedHost && !primaryTarget && !localContainerTarget) {
    throw new Error("Unsupported desktop chat stop route.");
  }
  const targetSwarmId = route?.swarmId?.trim() ?? "";
  const endpoint = managedHost
    ? "/v1/swarm/managed-hosts/sessions/stop"
    : primaryTarget
      ? `/v2/sessions/${encodeURIComponent(sessionId)}/run/stop/primary`
      : `/v2/sessions/${encodeURIComponent(sessionId)}/run/stop/local-container`;
  const body = managedHost
    ? { type: "run.stop", target_swarm_id: targetSwarmId, session_id: sessionId, run_id: runId }
    : primaryTarget
      ? { type: "run.stop", target_swarm_id: targetSwarmId, run_id: runId }
      : { type: "run.stop", run_id: runId };
  const response = await apiFetch(
    endpoint,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
    },
  );
  if (!response.ok) {
    throw new Error(await readErrorMessage(response));
  }
}

export async function resolveSessionPermission(
  sessionId: string,
  permissionId: string,
  action:
    | "approve"
    | "deny"
    | "approve_always"
    | "always_allow"
    | "always_deny",
  reason: string,
  approvedArguments?: Record<string, unknown>,
): Promise<DesktopPermissionRecord> {
  rejectV3SessionV2Subresource(sessionId, "session permissions");
  const response = await requestJson<ResolvePermissionResponseWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/permissions/${encodeURIComponent(permissionId)}/resolve`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        action,
        reason,
        approved_arguments: approvedArguments,
      }),
    },
  );
  return mapResolvedPermission(response.permission, response.saved_rule);
}

export async function fetchSessionUsageSummary(
  sessionId: string,
  signal?: AbortSignal,
): Promise<DesktopSessionUsageRecord | null> {
  rejectV3SessionV2Subresource(sessionId, "session usage summary");
  const response = await requestJson<SessionUsageResponseWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/usage`,
    { signal },
  );
  return mapSessionUsageSummary(response.usage_summary);
}

export async function fetchActiveSessionPlan(
  sessionId: string,
  signal?: AbortSignal,
): Promise<{ hasActive: boolean; plan: DesktopSessionPlanRecord }> {
  rejectV3SessionV2Subresource(sessionId, "session plans");
  const response = await requestJson<ActiveSessionPlanResponseWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/plans/active`,
    { signal },
  );
  return {
    hasActive: Boolean(response.has_active),
    plan: mapSessionPlan(response.active_plan),
  };
}

export async function activateSessionPlan(
  sessionId: string,
  planId: string,
): Promise<DesktopSessionPlanRecord> {
  rejectV3SessionV2Subresource(sessionId, "session plans");
  const response = await requestJson<ActiveSessionPlanResponseWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/plans/active`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ plan_id: planId.trim() }),
    },
  );
  return mapSessionPlan(response.active_plan);
}

export async function fetchSessionPlans(
  sessionId: string,
  signal?: AbortSignal,
): Promise<{ activePlanId: string; plans: DesktopSessionPlanRecord[] }> {
  rejectV3SessionV2Subresource(sessionId, "session plans");
  const response = await requestJson<SessionPlansResponseWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/plans?limit=100`,
    { signal },
  );
  return {
    activePlanId: String(response.active_plan_id ?? "").trim(),
    plans: Array.isArray(response.plans)
      ? response.plans.map((plan) => mapSessionPlan(plan))
      : [],
  };
}

export async function fetchSessionPlan(
  sessionId: string,
  planId: string,
  signal?: AbortSignal,
): Promise<DesktopSessionPlanRecord> {
  rejectV3SessionV2Subresource(sessionId, "session plans");
  const response = await requestJson<SaveSessionPlanResponseWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/plans/${encodeURIComponent(planId)}`,
    { signal },
  );
  return mapSessionPlan(response.plan);
}

export async function saveSessionPlan(
  sessionId: string,
  input: {
    id?: string;
    title?: string;
    plan?: string;
    document?: unknown;
    documentPatch?: unknown;
    status?: string;
    approvalState?: string;
  },
): Promise<DesktopSessionPlanRecord> {
  rejectV3SessionV2Subresource(sessionId, "session plans");
  const response = await requestJson<SaveSessionPlanResponseWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/plans`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        id: input.id?.trim() || undefined,
        plan_id: input.id?.trim() || undefined,
        title: input.title?.trim() || undefined,
        plan: input.plan,
        document: input.document ?? undefined,
        document_patch: input.documentPatch ?? undefined,
        status: input.status?.trim() || undefined,
        approval_state: input.approvalState?.trim() || undefined,
      }),
    },
  );
  return mapSessionPlan(response.plan);
}

export async function fetchSessionPlanHistory(
  sessionId: string,
  planId: string,
  signal?: AbortSignal,
): Promise<DesktopSessionPlanRevisionRecord[]> {
  rejectV3SessionV2Subresource(sessionId, "session plans");
  const response = await requestJson<SessionPlanHistoryResponseWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/plans/${encodeURIComponent(planId)}/history?limit=100`,
    { signal },
  );
  return Array.isArray(response.revisions)
    ? response.revisions.map((revision, index) => mapSessionPlanRevision(revision, index))
    : [];
}

export async function resolveAllSessionPermissions(
  sessionId: string,
  action:
    | "approve"
    | "deny"
    | "approve_always"
    | "always_allow"
    | "always_deny",
  reason: string,
  limit?: number,
): Promise<DesktopPermissionRecord[]> {
  rejectV3SessionV2Subresource(sessionId, "session permissions");
  const response = await requestJson<ResolveAllPermissionsResponseWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/permissions/resolve_all`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        action,
        reason,
        limit,
      }),
    },
  );
  return Array.isArray(response.resolved)
    ? response.resolved.map((permission) => mapResolvedPermission(permission))
    : [];
}

export async function fetchSessionPendingPermissions(
  sessionId: string,
  signal?: AbortSignal,
): Promise<DesktopPermissionRecord[]> {
  rejectV3SessionV2Subresource(sessionId, "session permissions");
  const response = await requestJson<PendingPermissionsResponseWire>(
    `/v2/sessions/${encodeURIComponent(sessionId)}/permissions?status=pending&limit=200`,
    { signal },
  );
  return Array.isArray(response.permissions)
    ? response.permissions
        .map((permission) => mapResolvedPermission(permission))
        .filter(
          (permission) =>
            permission.id !== "" &&
            permission.sessionId !== "" &&
            permission.status === "pending",
        )
    : [];
}

function modelOptionKey(
  provider: string,
  model: string,
  contextMode = "",
): string {
  return `${provider}:${model}:${contextMode.trim().toLowerCase()}`;
}

export async function fetchModelOptions(
  signal?: AbortSignal,
): Promise<ModelOptionRecord[]> {
  const providersResponse = await requestJson<ProvidersResponseWire>(
    "/v1/providers",
    { signal },
  );
  const providers = Array.isArray(providersResponse.providers)
    ? providersResponse.providers
        .filter(
          (provider) => Boolean(provider.ready) && Boolean(provider.runnable),
        )
        .map((provider) => String(provider.id ?? "").trim())
        .filter(Boolean)
    : [];

  const [favoritesByProvider, catalogByProvider] = await Promise.all([
    Promise.all(
      providers.map(
        async (provider) =>
          [
            provider,
            await requestJson<FavoritesResponseWire>(
              `/v1/models/favorites?provider=${encodeURIComponent(provider)}&limit=200`,
              { signal },
            ),
          ] as const,
      ),
    ),
    Promise.all(
      providers.map(
        async (provider) =>
          [
            provider,
            await requestJson<CatalogResponseWire>(
              `/v1/model/catalog?provider=${encodeURIComponent(provider)}&limit=200`,
              { signal },
            ),
          ] as const,
      ),
    ),
  ]);

  const options = new Map<string, ModelOptionRecord>();

  for (const [provider, response] of favoritesByProvider) {
    for (const record of response.records ?? []) {
      const model = String(record.model ?? "").trim();
      if (!model || !modelAllowedByProviderPreset(provider, model)) {
        continue;
      }
      const key = modelOptionKey(provider, model);
      options.set(key, {
        key,
        provider,
        model,
        contextMode: "",
        label: String(record.label ?? `${provider}/${model}`).trim(),
        thinking: String(record.thinking ?? "").trim(),
        favorite: true,
        contextWindow: 0,
      });
    }
  }

  for (const [provider, response] of catalogByProvider) {
    for (const record of response.records ?? []) {
      const model = String(record.model ?? "").trim();
      if (!model || !modelAllowedByProviderPreset(provider, model)) {
        continue;
      }
      const key = modelOptionKey(provider, model);
      const current = options.get(key);
      if (!current) {
        options.set(key, {
          key,
          provider,
          model,
          contextMode: "",
          label: `${provider}/${model}`,
          thinking: "",
          favorite: false,
          contextWindow:
            typeof record.context_window === "number"
              ? record.context_window
              : 0,
        });
        continue;
      }
      options.set(key, {
        ...current,
        contextWindow:
          typeof record.context_window === "number"
            ? record.context_window
            : current.contextWindow,
      });
    }
  }

  for (const option of Array.from(options.values())) {
    if (!supportsCodex1MMode(option.provider, option.model)) {
      continue;
    }
    const contextMode = "1m";
    const key = modelOptionKey(option.provider, option.model, contextMode);
    if (options.has(key)) {
      continue;
    }
    options.set(key, {
      ...option,
      key,
      contextMode,
    });
  }

  return sortModelOptions(Array.from(options.values()));
}
