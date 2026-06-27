import type { QueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  readErrorMessage,
  requestJson,
} from "../../../../app/api";
import type {
  DesktopPermissionRecord,
  DesktopRunIntentRecord,
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
  DesktopSessionPlanCheckpoint,
  DesktopSessionPlanDocument,
  DesktopSessionPlanRecord,
  DesktopSessionPlanRevisionRecord,
} from "../types/chat";
import {
  applyDesktopChatRouteToSession,
  getDesktopSessionCreateTarget,
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
import { normalizeDesktopPermission } from "../../permissions/services/desktop-permission-normalization";

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
  applied_seq?: number;
  high_watermark?: number;
  oldest_seq?: number;
  newest_seq?: number;
  next_before_seq?: number;
  next_after_seq?: number;
  has_more?: boolean;
  has_more_older?: boolean;
  has_more_newer?: boolean;
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
  pending_permissions?: ResolvePermissionResponseWire["permission"][];
  usage_summary?: SessionUsageSummaryWire | null;
  active_run_intent?: V3RunIntentWire | null;
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

interface V3RealtimeOutboxWire {
  endpoint_seq?: number;
  endpoint_cursor?: string;
  session_id?: string;
}

interface V3MutationWire {
  realtime_outbox?: V3RealtimeOutboxWire | null;
  session?: never;
  messages?: never;
  events?: never;
  workset_id?: never;
  worksets?: never;
  subscriptions?: never;
}

interface V3MessageCommitResponseWire extends V3HydratedSessionResponseWire {
  ok?: boolean;
  message?: MessageWire;
  run_intent?: V3RunIntentWire | null;
  realtime_outbox?: V3RealtimeOutboxWire | null;
  mutation?: V3MutationWire | null;
}

interface V3CompactResponseWire {
  ok?: boolean;
  session_id?: string;
  run_id?: string;
  status?: string;
  error?: string;
  run_intent?: V3RunIntentWire | null;
  compaction?: {
    run_id?: string;
    status?: string;
    owner_transport?: string;
  };
  terminal?: {
    event_type?: string;
    phase?: string;
  };
  mutation?: V3MutationWire | null;
  realtime_outbox?: V3RealtimeOutboxWire | null;
}

export type V3RunIntentRecord = DesktopRunIntentRecord;

export interface SendSessionMessageResult {
  ok?: boolean;
  session?: DesktopSessionRecord;
  message?: ChatMessageRecord | null;
  messages?: ChatMessageRecord[];
  runIntent?: V3RunIntentRecord | null;
  events?: unknown[];
  realtimeOutbox?: {
    endpointSeq: number;
    endpointCursor: string;
    sessionId: string;
  } | null;
}

export interface CompactSessionV3Result {
  ok?: boolean;
  sessionId?: string;
  runId?: string;
  status?: string;
  error?: string;
  ownerTransport?: string;
  terminal?: {
    eventType: string;
    phase: string;
  };
  session?: DesktopSessionRecord;
  runIntent?: V3RunIntentRecord | null;
  realtimeOutbox?: SendSessionMessageResult['realtimeOutbox'];
  assistantMessage?: ChatMessageRecord | null;
  usageSummary?: DesktopSessionUsageRecord | null;
  events?: unknown[];
}

export interface FetchSessionMessagesResult {
  messages: ChatMessageRecord[];
  appliedSeq: number;
  highWatermark: number;
  oldestSeq: number;
  newestSeq: number;
  nextBeforeSeq: number;
  nextAfterSeq: number;
  hasMore: boolean;
  hasMoreOlder: boolean;
  hasMoreNewer: boolean;
}

interface SessionDataRequestOptions {
  sessionApi?: string | null;
}

function resolveSessionApiForSession(
  _sessionId: string,
  options: SessionDataRequestOptions = {},
): string {
  const sessionApi = options.sessionApi?.trim().toLowerCase() ?? "";
  if (sessionApi && sessionApi !== "v3") {
    throw new Error("Desktop sessions only support Sessions API v3.");
  }
  return "v3";
}

function rejectLegacyDesktopSessionPath(_sessionId: string, subresource: string): never {
  throw new Error(`Legacy desktop session ${subresource} is disabled; use Sessions API v3.`);
}

interface SendSessionMessageOptions extends SessionDataRequestOptions {
  clientRequestId?: string | null;
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

interface SessionPlanExecutionPolicyWire {
  mode?: string;
  shape?: string;
}

interface SessionPlanExecutionStateWire {
  status?: string;
  active_attempt_id?: string;
  parent_session_id?: string;
  current_session_id?: string;
  current_run_id?: string;
  last_checkpoint_id?: string;
  last_attempt_id?: string;
  last_outcome?: string;
  started_at?: number;
  updated_at?: number;
  completed_at?: number;
}

interface SessionPlanCheckpointReviewWire {
  status?: string;
  reviewer_id?: string;
  reviewer_type?: string;
  result?: string;
  notes?: string;
  reviewed_at?: number;
}

interface SessionPlanCheckpointAttemptWire {
  id?: string;
  checkpoint_id?: string;
  status?: string;
  outcome?: string;
  run_id?: string;
  session_id?: string;
  parent_session_id?: string;
  started_at?: number;
  completed_at?: number;
  report?: string;
  result?: string;
  changed_files?: string[];
  validation?: string[];
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
  attempt_id?: string;
  run_id?: string;
  session_id?: string;
  started_at?: number;
  completed_at?: number;
  review?: SessionPlanCheckpointReviewWire | null;
  attempts?: SessionPlanCheckpointAttemptWire[] | null;
  order?: number;
}

interface SessionPlanDocumentWire {
  id?: string;
  title?: string;
  status?: string;
  schema_version?: string;
  revision_id?: string;
  info?: SessionPlanInfoWire | null;
  execution_policy?: SessionPlanExecutionPolicyWire | null;
  execution_state?: SessionPlanExecutionStateWire | null;
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
    sidebarToolName: null,
    toolCallId: null,
    toolArguments: null,
    toolOutput: "",
    retainedToolName: null,
    retainedToolCallId: null,
    retainedToolArguments: null,
    retainedToolOutput: "",
    retainedToolState: null,
    toolHistory: [],
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
    executionPolicy: mapSessionPlanExecutionPolicy(document.execution_policy),
    executionState: mapSessionPlanExecutionState(document.execution_state),
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
          attemptId: String(checkpoint?.attempt_id ?? "").trim(),
          runId: String(checkpoint?.run_id ?? "").trim(),
          sessionId: String(checkpoint?.session_id ?? "").trim(),
          startedAt: typeof checkpoint?.started_at === "number" ? checkpoint.started_at : 0,
          completedAt: typeof checkpoint?.completed_at === "number" ? checkpoint.completed_at : 0,
          review: mapSessionPlanCheckpointReview(checkpoint?.review),
          attempts: mapSessionPlanCheckpointAttempts(checkpoint?.attempts),
          order: typeof checkpoint?.order === "number" ? checkpoint.order : index + 1,
        }))
      : [],
    activeCheckpointId: String(document.active_checkpoint_id ?? "").trim(),
    renderedText: String(document.rendered_text ?? ""),
    displayText: String(document.display_text ?? ""),
  };
}

export function mapDesktopSessionPlan(
  value: unknown,
): DesktopSessionPlanRecord {
  const plan = value as SessionPlanWire | null | undefined;
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

function mapSessionPlanExecutionPolicy(policy: SessionPlanExecutionPolicyWire | null | undefined): DesktopSessionPlanDocument['executionPolicy'] {
  if (!policy) return null;
  const mapped = {
    mode: String(policy.mode ?? "").trim(),
    shape: String(policy.shape ?? "").trim(),
  };
  return mapped.mode || mapped.shape ? mapped : null;
}

function mapSessionPlanExecutionState(state: SessionPlanExecutionStateWire | null | undefined): DesktopSessionPlanDocument['executionState'] {
  if (!state) return null;
  const mapped = {
    status: String(state.status ?? "").trim(),
    activeAttemptId: String(state.active_attempt_id ?? "").trim(),
    parentSessionId: String(state.parent_session_id ?? "").trim(),
    currentSessionId: String(state.current_session_id ?? "").trim(),
    currentRunId: String(state.current_run_id ?? "").trim(),
    lastCheckpointId: String(state.last_checkpoint_id ?? "").trim(),
    lastAttemptId: String(state.last_attempt_id ?? "").trim(),
    lastOutcome: String(state.last_outcome ?? "").trim(),
    startedAt: typeof state.started_at === "number" ? state.started_at : 0,
    updatedAt: typeof state.updated_at === "number" ? state.updated_at : 0,
    completedAt: typeof state.completed_at === "number" ? state.completed_at : 0,
  };
  return mapped.status || mapped.activeAttemptId || mapped.currentRunId || mapped.lastCheckpointId ? mapped : null;
}

function mapSessionPlanCheckpointReview(review: SessionPlanCheckpointReviewWire | null | undefined): DesktopSessionPlanCheckpoint["review"] {
  if (!review) return null;
  const mapped = {
    status: String(review.status ?? "").trim(),
    reviewerId: String(review.reviewer_id ?? "").trim(),
    reviewerType: String(review.reviewer_type ?? "").trim(),
    result: String(review.result ?? "").trim(),
    notes: String(review.notes ?? "").trim(),
    reviewedAt: typeof review.reviewed_at === "number" ? review.reviewed_at : 0,
  };
  return mapped.status || mapped.reviewerId || mapped.result || mapped.notes || mapped.reviewedAt > 0 ? mapped : null;
}

function mapSessionPlanCheckpointAttempts(attempts: SessionPlanCheckpointAttemptWire[] | null | undefined): DesktopSessionPlanCheckpoint["attempts"] {
  if (!Array.isArray(attempts)) return [];
  return attempts.map((attempt) => ({
    id: String(attempt?.id ?? "").trim(),
    checkpointId: String(attempt?.checkpoint_id ?? "").trim(),
    status: String(attempt?.status ?? "").trim(),
    outcome: String(attempt?.outcome ?? "").trim(),
    runId: String(attempt?.run_id ?? "").trim(),
    sessionId: String(attempt?.session_id ?? "").trim(),
    parentSessionId: String(attempt?.parent_session_id ?? "").trim(),
    startedAt: typeof attempt?.started_at === "number" ? attempt.started_at : 0,
    completedAt: typeof attempt?.completed_at === "number" ? attempt.completed_at : 0,
    report: String(attempt?.report ?? "").trim(),
    result: String(attempt?.result ?? "").trim(),
    changedFiles: mapStringArray(attempt?.changed_files),
    validation: mapStringArray(attempt?.validation),
  }));
}

export function mapDesktopSessionPlanRevision(
  value: unknown,
  index: number,
): DesktopSessionPlanRevisionRecord {
  const plan = value as SessionPlanWire | null | undefined;
  const base = mapDesktopSessionPlan(plan);
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

export function mapDesktopSession(session: unknown): DesktopSessionRecord {
  return mapSession(session as SessionWire);
}

export function mapDesktopSessionUsageSummary(summary: unknown): DesktopSessionUsageRecord | null {
  return mapSessionUsageSummary(summary as SessionUsageSummaryWire | null | undefined);
}

export function mapDesktopSessionPermission(permission: unknown, expectedSessionId = ''): DesktopPermissionRecord | null {
  return normalizeDesktopPermission(permission, expectedSessionId);
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

function mapV3RunIntent(intent: V3RunIntentWire | null | undefined): DesktopRunIntentRecord | null {
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

function mapV3RealtimeOutbox(row: V3RealtimeOutboxWire | null | undefined): SendSessionMessageResult['realtimeOutbox'] {
  if (!row || typeof row !== 'object') {
    return null;
  }
  return {
    endpointSeq: typeof row.endpoint_seq === 'number' ? row.endpoint_seq : 0,
    endpointCursor: String(row.endpoint_cursor ?? '').trim(),
    sessionId: String(row.session_id ?? '').trim(),
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
    realtimeOutbox: mapV3RealtimeOutbox(response.realtime_outbox ?? response.mutation?.realtime_outbox),
  };
}

function mapV3CompactResponse(response: V3CompactResponseWire): CompactSessionV3Result {
  return {
    ok: response.ok,
    sessionId: String(response.session_id ?? '').trim(),
    runId: String(response.run_id ?? response.compaction?.run_id ?? response.run_intent?.run_id ?? '').trim(),
    status: String(response.status ?? response.compaction?.status ?? response.run_intent?.status ?? '').trim(),
    error: typeof response.error === 'string' ? response.error.trim() : undefined,
    ownerTransport: String(response.compaction?.owner_transport ?? '').trim(),
    terminal: response.terminal && typeof response.terminal === 'object'
      ? {
          eventType: String(response.terminal.event_type ?? '').trim(),
          phase: String(response.terminal.phase ?? '').trim(),
        }
      : undefined,
    runIntent: mapV3RunIntent(response.run_intent),
    realtimeOutbox: mapV3RealtimeOutbox(response.realtime_outbox ?? response.mutation?.realtime_outbox),
    assistantMessage: null,
    usageSummary: null,
    events: [],
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
  const terminalLifecycleSummary = normalizedLifecyclePhase === "errored"
    ? (lifecycle?.error ?? lifecycle?.stopReason ?? null)
    : (lifecycle?.stopReason ?? null);
  const terminalLifecycleError = normalizedLifecyclePhase === "errored"
    ? (lifecycle?.error ?? lifecycle?.stopReason ?? null)
    : null;
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
    runIntent: null,
    live: {
      ...emptyLiveState(),
      lastEventAt: lifecycle?.updatedAt ? lifecycle.updatedAt : null,
      summary: terminalLifecycleSummary,
      error: terminalLifecycleError,
    },
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
  };
}


function mapResolvedPermission(
  permission: ResolvePermissionResponseWire["permission"],
  savedRule?: ResolvePermissionResponseWire["saved_rule"],
  expectedSessionId = '',
): DesktopPermissionRecord | null {
  return normalizeDesktopPermission(
    savedRule ? { ...(permission ?? {}), saved_rule: savedRule } : permission,
    expectedSessionId,
  );
}

export async function fetchSessionMessages(
  sessionId: string,
  signal?: AbortSignal,
  afterSeq = 0,
  options: SessionDataRequestOptions & { beforeSeq?: number; limit?: number; queryClient?: QueryClient; tail?: boolean } = {},
): Promise<FetchSessionMessagesResult> {
  const normalizedSessionId = sessionId.trim();
  const search = new URLSearchParams();
  const beforeSeq = options.beforeSeq && options.beforeSeq > 0 ? Math.floor(options.beforeSeq) : 0;
  const tail = Boolean(options.tail && beforeSeq <= 0 && afterSeq <= 0);
  if (tail) {
    search.set("tail", "true");
  }
  search.set("limit", String(options.limit && options.limit > 0 ? Math.floor(options.limit) : 100));
  if (beforeSeq > 0) {
    search.set("before_seq", String(beforeSeq));
  } else if (afterSeq > 0) {
    search.set("after_seq", String(afterSeq));
  }
  resolveSessionApiForSession(normalizedSessionId, options);
  const response = await requestJson<MessagesResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/messages?${search.toString()}`,
    { signal },
  );
  const messages = Array.isArray(response.messages)
    ? response.messages.map(mapChatMessage)
    : [];
  const newestSeq = typeof response.newest_seq === "number"
    ? response.newest_seq
    : messages.reduce((max, message) => Math.max(max, message.globalSeq), 0);
  const oldestSeq = typeof response.oldest_seq === "number"
    ? response.oldest_seq
    : messages.reduce((min, message) => min === 0 ? message.globalSeq : Math.min(min, message.globalSeq), 0);
  const result = {
    messages,
    appliedSeq: typeof response.applied_seq === "number" ? Math.max(0, response.applied_seq) : newestSeq,
    highWatermark: typeof response.high_watermark === "number" ? Math.max(0, response.high_watermark) : newestSeq,
    oldestSeq,
    newestSeq,
    nextBeforeSeq: typeof response.next_before_seq === "number" ? response.next_before_seq : oldestSeq,
    nextAfterSeq: typeof response.next_after_seq === "number" ? response.next_after_seq : newestSeq,
    hasMore: Boolean(response.has_more),
    hasMoreOlder: Boolean(response.has_more_older),
    hasMoreNewer: Boolean(response.has_more_newer),
  };
  return result;
}

export async function fetchSessionPreference(
  sessionId: string,
  signal?: AbortSignal,
): Promise<ResolvedSessionPreference> {
  void signal;
  return rejectLegacyDesktopSessionPath(sessionId, "preference");
}

export async function fetchSessionMode(
  sessionId: string,
  signal?: AbortSignal,
): Promise<string> {
  void signal;
  return rejectLegacyDesktopSessionPath(sessionId, "mode");
}

export async function updateSessionMode(
  sessionId: string,
  mode: string,
): Promise<string> {
  void mode;
  return rejectLegacyDesktopSessionPath(sessionId, "mode update");
}

export async function fetchSessionCodexConfig(
  sessionId: string,
  signal?: AbortSignal,
): Promise<DesktopSessionCodexConfig> {
  void signal;
  return rejectLegacyDesktopSessionPath(sessionId, "Codex config");
}

export async function updateSessionCodexConfig(
  sessionId: string,
  input: { serviceTier?: string; contextMode?: string },
): Promise<DesktopSessionCodexConfig> {
  void input;
  return rejectLegacyDesktopSessionPath(sessionId, "Codex config update");
}

export async function updateSessionPreference(
  sessionId: string,
  input: Partial<ResolvedSessionPreference["preference"]>,
): Promise<ResolvedSessionPreference> {
  void input;
  return rejectLegacyDesktopSessionPath(sessionId, "preference update");
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
  return mapAgentStateResponse(response);
}

export async function fetchAgentStateSummary(
  signal?: AbortSignal,
): Promise<AgentStateRecord> {
  const response = await requestJson<AgentStateWire>("/v2/agents?limit=200&view=summary", {
    signal,
  });
  return mapAgentStateResponse(response);
}

function mapAgentStateResponse(response: AgentStateWire): AgentStateRecord {
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

const SESSION_CREATE_FORBIDDEN_METADATA_KEYS = new Set([
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

const SESSION_CREATE_FORBIDDEN_METADATA_PREFIXES = [
  'swarm_route_',
  'swarm_routed_',
  'swarm_managed_',
  'swarm_v2_',
  'hosted_session',
  'managed_host',
]

const SESSION_CREATE_FORBIDDEN_METADATA_PARTS = [
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

function isForbiddenSessionCreateMetadataKey(key: string): boolean {
  const normalized = key.trim().toLowerCase()
  if (!normalized) {
    return true
  }
  if (SESSION_CREATE_FORBIDDEN_METADATA_KEYS.has(normalized)) {
    return true
  }
  if (SESSION_CREATE_FORBIDDEN_METADATA_PREFIXES.some((prefix) => normalized.startsWith(prefix))) {
    return true
  }
  return SESSION_CREATE_FORBIDDEN_METADATA_PARTS.some((part) => normalized.includes(part))
}

function sanitizeSessionCreateMetadataValue(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map((child) => sanitizeSessionCreateMetadataValue(child))
  }
  if (!value || typeof value !== 'object') {
    return value
  }
  const sanitized: Record<string, unknown> = {}
  for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
    if (isForbiddenSessionCreateMetadataKey(key)) {
      continue
    }
    sanitized[key] = sanitizeSessionCreateMetadataValue(child)
  }
  return sanitized
}

export function sanitizeSessionCreateMetadata(metadata: Record<string, unknown> | null | undefined): Record<string, unknown> | undefined {
  if (!metadata || typeof metadata !== 'object') {
    return undefined
  }
  const sanitized = sanitizeSessionCreateMetadataValue(metadata) as Record<string, unknown>
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

function sessionCreateV3RequestBody(input: {
  target: { swarmId: string; workspaceBindingId: string };
  title?: string;
  workspacePath: string;
  workspaceName: string;
  mode: string;
  agentName?: string;
  metadata?: Record<string, unknown>;
  preference: ResolvedSessionPreference["preference"];
  worktreeMode?: string;
  worktreeUseCurrentBranch?: boolean;
  worktreeBaseBranch?: string;
  worktreeBranchName?: string;
}): Record<string, unknown> {
  const preference = sessionCreatePreferenceBody(input.preference)
  const title = optionalString(input.title)
  return stripUndefinedFields({
    client_request_id: `desktop-v3-create:${crypto.randomUUID()}`,
    swarm_id: input.target.swarmId,
    workspace_binding_id: input.target.workspaceBindingId,
    title: title || undefined,
    workspace_path: input.workspacePath,
    workspace_name: input.workspaceName,
    mode: input.mode,
    agent_name: input.agentName?.trim() || undefined,
    preference: Object.keys(preference).length > 0 ? preference : undefined,
    metadata: sanitizeSessionCreateMetadata(input.metadata),
    worktree_mode: optionalString(input.worktreeMode) || undefined,
    worktree_use_current_branch: typeof input.worktreeUseCurrentBranch === "boolean" ? input.worktreeUseCurrentBranch : undefined,
    worktree_base_branch: optionalString(input.worktreeBaseBranch) || undefined,
    worktree_branch_name: optionalString(input.worktreeBranchName) || undefined,
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
  const target = getDesktopSessionCreateTarget(input.route)
  if (target.endpoint === null) {
    throw new Error(target.unsupportedReason)
  }
  const body = sessionCreateV3RequestBody({
    target: { swarmId: target.swarmId, workspaceBindingId: target.workspaceBindingId },
    title: input.title,
    workspacePath: input.workspacePath,
    workspaceName: input.workspaceName,
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

export async function compactSessionV3(
  sessionId: string,
  options: { note?: string | null; agentName?: string | null; instructions?: string | null; clientRequestId?: string | null } = {},
): Promise<CompactSessionV3Result> {
  const normalizedSessionId = sessionId.trim();
  if (!normalizedSessionId) {
    throw new Error("session id is required");
  }
  const clientRequestId = options.clientRequestId?.trim()
    || `desktop-v3-compact:${normalizedSessionId}:${crypto.randomUUID()}`;
  const response = await apiFetch(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/compact`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        client_request_id: clientRequestId,
        note: options.note?.trim() ?? "",
        agent_name: options.agentName?.trim() ?? "",
        instructions: options.instructions?.trim() ?? "",
      }),
    },
  );
  if (!response.ok) {
    let payload: V3CompactResponseWire | null = null;
    try {
      payload = await response.clone().json() as V3CompactResponseWire;
    } catch {
      throw new Error(await readErrorMessage(response));
    }
    const mapped = mapV3CompactResponse(payload);
    if (mapped.realtimeOutbox?.endpointCursor) return mapped;
    throw new Error(mapped.error || await readErrorMessage(response));
  }
  return mapV3CompactResponse(await response.json() as V3CompactResponseWire);
}

export async function sendSessionMessage(
  sessionId: string,
  role: "user" | "assistant" | "system" | "tool" | "reasoning",
  content: string,
  route?: DesktopChatRoute | null,
  options: SendSessionMessageOptions = {},
): Promise<SendSessionMessageResult> {
  const normalizedSessionId = sessionId.trim();
  void route;
  resolveSessionApiForSession(normalizedSessionId, options);
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
  options: SessionDataRequestOptions = {},
): Promise<DesktopPermissionRecord | null> {
  const sessionApi = resolveSessionApiForSession(sessionId, options);
  if (sessionApi !== "v3") {
    throw new Error("Desktop permission resolution requires explicit Sessions API v3 context");
  }
  const response = await requestJson<ResolvePermissionResponseWire>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/permissions/${encodeURIComponent(permissionId)}/resolve`,
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
  return mapResolvedPermission(response.permission, response.saved_rule, sessionId);
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
  options: SessionDataRequestOptions = {},
): Promise<DesktopPermissionRecord[]> {
  const sessionApi = resolveSessionApiForSession(sessionId, options);
  if (sessionApi !== "v3") {
    throw new Error("Desktop bulk permission resolution requires explicit Sessions API v3 context");
  }
  const response = await requestJson<ResolveAllPermissionsResponseWire>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/permissions/resolve_all`,
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
    ? response.resolved
      .map((permission) => mapResolvedPermission(permission, undefined, sessionId))
      .filter((permission): permission is DesktopPermissionRecord => Boolean(permission))
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
